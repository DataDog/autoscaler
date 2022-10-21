/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package metrics

import (
	"context"
	k8sapiv1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	externalmetricsv1beta1 "k8s.io/metrics/pkg/apis/external_metrics/v1beta1"
	"k8s.io/metrics/pkg/apis/metrics/v1beta1"
	"time"

	resourceclient "k8s.io/metrics/pkg/client/clientset/versioned/typed/metrics/v1beta1"
	"k8s.io/metrics/pkg/client/external_metrics"
)

// PodMetricsLister wraps both metrics-client and External Metrics
type PodMetricsLister interface {
	List(ctx context.Context, namespace string, opts v1.ListOptions) (*v1beta1.PodMetricsList, error)
}

type podMetricsSource struct {
	metricsGetter resourceclient.PodMetricsesGetter
}

// NewPodMetricsesSource Returns a Source-wrapper around PodMetricsesGetter.
func NewPodMetricsesSource(source resourceclient.PodMetricsesGetter) PodMetricsLister {
	return &podMetricsSource{metricsGetter: source}
}

func (s *podMetricsSource) List(ctx context.Context, namespace string, opts v1.ListOptions) (*v1beta1.PodMetricsList, error) {
	podMetricsInterface := s.metricsGetter.PodMetricses(namespace)
	return podMetricsInterface.List(ctx, opts)
}

type externalMetricsClient struct {
	externalClient external_metrics.ExternalMetricsClient
	options        ExternalClientOptions
	clusterState   *model.ClusterState
}

// ExternalClientOptions specifies parameters for using an External Metrics Client.
type ExternalClientOptions struct {
	ResourceMetrics                                  map[k8sapiv1.ResourceName]string
	PodNamespaceLabel, PodNameLabel                  string
	CtrNamespaceLabel, CtrPodNameLabel, CtrNameLabel string
}

// NewExternalClient returns a Source for an External Metrics Client.
func NewExternalClient(c *rest.Config, clusterState *model.ClusterState, options ExternalClientOptions) PodMetricsLister {
	extClient, err := external_metrics.NewForConfig(c)
	if err != nil {
		klog.Fatalf("Failed initializing external metrics client: %v", err)
	}
	return &externalMetricsClient{
		externalClient: extClient,
		options:        options,
		clusterState:   clusterState,
	}
}

func (s *externalMetricsClient) containerId(value externalmetricsv1beta1.ExternalMetricValue) *model.ContainerID {
	podNS, hasPodNS := value.MetricLabels[s.options.PodNamespaceLabel]
	podName, hasPodName := value.MetricLabels[s.options.PodNameLabel]
	ctrName, hasCtrName := value.MetricLabels[s.options.CtrNameLabel]
	if hasPodNS && hasPodName && hasCtrName {
		return &model.ContainerID{
			PodID:         model.PodID{Namespace: podNS, PodName: podName},
			ContainerName: ctrName,
		}
	}
	return nil
}

type podContainerResourceMap map[model.PodID]map[string]k8sapiv1.ResourceList

func (s *externalMetricsClient) addMetrics(list *externalmetricsv1beta1.ExternalMetricValueList, name k8sapiv1.ResourceName, resourceMap podContainerResourceMap) {
	for _, val := range list.Items {
		if id := s.containerId(val); id != nil {
			resourceMap[id.PodID][id.ContainerName][name] = val.Value
		}
	}
}

func (s *externalMetricsClient) List(ctx context.Context, namespace string, opts v1.ListOptions) (*v1beta1.PodMetricsList, error) {
	result := v1beta1.PodMetricsList{}
	// Get all VPAs in the namespace
	// - We already do this in the cluster state feeder!  It's in its clusterState member.
	//   We just have to feed it into here somehow.
	// - use the 'PodSelector' there as the input to the external api.
	// Send out the queries.
	nsClient := s.externalClient.NamespacedMetrics(namespace)

	for _, vpa := range s.clusterState.Vpas {
		if vpa.PodCount == 0 {
			continue
		}
		if namespace != "" && vpa.ID.Namespace != namespace {
			continue
		}
		workloadValues := make(podContainerResourceMap)

		var selectedTimestamp v1.Time
		var selectedWindows time.Duration
		for resourceName, metricName := range s.options.ResourceMetrics {
			m, err := nsClient.List(metricName, vpa.PodSelector)
			if err != nil {
				return nil, err // Do we want to error or do we prefer to skip ?
			}
			if m == nil || len(m.Items) == 0 {
				continue
			}
			s.addMetrics(m, resourceName, workloadValues)

			if !m.Items[0].Timestamp.Time.IsZero() {
				selectedTimestamp = m.Items[0].Timestamp
				if m.Items[0].WindowSeconds != nil {
					selectedWindows = time.Duration(*m.Items[0].WindowSeconds) * time.Second
				}
			}
		}

		for podId, cmaps := range workloadValues {
			podMets := v1beta1.PodMetrics{
				TypeMeta:   v1.TypeMeta{},
				ObjectMeta: v1.ObjectMeta{Name: podId.PodName, Namespace: podId.Namespace},
				Timestamp:  selectedTimestamp, // I am not sure this is correct. Need to rework Timestamp and Window... which one should we use?
				Window:     v1.Duration{Duration: selectedWindows},
				Containers: make([]v1beta1.ContainerMetrics, len(cmaps)),
			}
			for cname, res := range cmaps {
				podMets.Containers = append(podMets.Containers, v1beta1.ContainerMetrics{Name: cname, Usage: res})
			}
			result.Items = append(result.Items, podMets)
		}
	}
	return &result, nil
}
