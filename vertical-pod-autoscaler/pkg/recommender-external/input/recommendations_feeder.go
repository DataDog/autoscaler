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

package input

import (
	"strings"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"k8s.io/metrics/pkg/client/external_metrics"

	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender-external/input/metrics"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender-external/model"
	upstream_model "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
)

// ExternalRecommendationsStateFeeder can update state of ExternalRecommendations object.
type ExternalRecommendationsStateFeeder interface {
	// LoadVPAs updates externalRecommendationsState with current state of VPAs.
	LoadVPAs()

	// LoadMetrics updates externalRecommendationsState with current usage metrics of containers.
	LoadMetrics()
}

// ExternalRecommendationsStateFeederFactory makes instances of ExternalRecommendationsStateFeeder.
type ExternalRecommendationsStateFeederFactory struct {
	ExternalRecommendationsState *model.ExternalRecommendationsState
	ClusterState                 *upstream_model.ClusterState
	MetricsClient                metrics.MetricsClient
}

type externalRecommendationsStateFeeder struct {
	externalRecommendationsState *model.ExternalRecommendationsState
	clusterState                 *upstream_model.ClusterState
	metricsClient                metrics.MetricsClient
}

// Make creates new ClusterStateFeeder with internal data providers, based on kube client.
func (m ExternalRecommendationsStateFeederFactory) Make() ExternalRecommendationsStateFeeder {
	return &externalRecommendationsStateFeeder{
		metricsClient:                m.MetricsClient,
		clusterState:                 m.ClusterState,
		externalRecommendationsState: m.ExternalRecommendationsState,
	}
}

// NewExternalRecommendationsStateFeeder creates new ExternalRecommendationsStateFeeder with internal data providers.
func NewExternalRecommendationsStateFeeder(config *rest.Config, clusterState *upstream_model.ClusterState, externalRecommendationsState *model.ExternalRecommendationsState, namespace, metricsClientName string) ExternalRecommendationsStateFeeder {
	return ExternalRecommendationsStateFeederFactory{
		MetricsClient:                newMetricsClient(config, namespace, metricsClientName),
		ClusterState:                 clusterState,
		ExternalRecommendationsState: externalRecommendationsState,
	}.Make()
}

func (e externalRecommendationsStateFeeder) LoadVPAs() {
	// Add or update existing VPAs in the model.
	vpaKeys := make(map[upstream_model.VpaID]bool)
	for _, vpa := range e.clusterState.Vpas {
		e.externalRecommendationsState.AddOrUpdateVpa(*vpa)
		vpaKeys[vpa.ID] = true
	}

	klog.V(3).Infof("Updated %d VPAs.", len(vpaKeys))

	// Delete non-existent VPAs from the model.
	for vpaID := range e.externalRecommendationsState.Vpas {
		if _, exists := vpaKeys[vpaID]; !exists {
			klog.V(3).Infof("Deleting VPA %v", vpaID)
			if err := e.externalRecommendationsState.DeleteVpa(vpaID); err != nil {
				klog.Errorf("Deleting VPA %v failed: %v", vpaID, err)
			}
		}
	}
}

func (e externalRecommendationsStateFeeder) LoadMetrics() {
	for _, vpa := range e.clusterState.Vpas {
		klog.V(1).Infof("vpa %s %d", vpa.ID.VpaName, vpa.PodCount)

		containersToResourcesAndMetrics := GetVpaExternalMetrics(vpa.Annotations)
		errs := e.loadMetrics(vpa, containersToResourcesAndMetrics)

		for _, err := range errs {
			// TODO: Add an event to the VPA object.
			// TODO: Add a metric to track this.
			klog.V(0).ErrorS(err, "Got an error")
		}
	}
}

func (e externalRecommendationsStateFeeder) loadMetrics(vpa *upstream_model.Vpa, containersToResourcesAndMetrics ContainersToResourcesAndMetrics) []error {
	var errs []error

	for container, resourceToMetrics := range containersToResourcesAndMetrics {
		recommendation := make(upstream_model.Resources)
		for resource, metric := range resourceToMetrics {
			value, err := e.loadMetric(vpa, resource, metric)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			recommendation[resource] = value
		}
		if len(recommendation) == 0 {
			klog.V(1).Infof("Got an empty recommendation for VPA %s, Container %s: %+v", vpa.ID, container, resourceToMetrics)
		}
		e.externalRecommendationsState.AddContainerRecommendation(vpa.ID, container, recommendation)
	}
	return errs
}

func (e externalRecommendationsStateFeeder) loadMetric(vpa *upstream_model.Vpa, resource upstream_model.ResourceName, metric string) (upstream_model.ResourceAmount, error) {
	metricName, metricSelector, err := e.getMetricNameAndSelector(metric)
	if err != nil {
		return 0, err
	}

	value, _, err := e.metricsClient.GetExternalMetric(metricName, vpa.ID.Namespace, metricSelector)
	if err != nil {
		return 0, err
	}

	return upstream_model.ResourceAmount(value), nil
}

func (e externalRecommendationsStateFeeder) getMetricNameAndSelector(metric string) (string, labels.Selector, error) {
	metricName := metric
	metricSelector := labels.Everything()

	// If there's a `{` we assume it's a metric selector.
	if i := strings.Index(metric, "{"); i != -1 {
		metricName = metric[:i]
		labelSelector, err := v1.ParseToLabelSelector(metric[i:])
		if err != nil {
			return "", nil, err
		}
		metricSelector, err = v1.LabelSelectorAsSelector(labelSelector)
		if err != nil {
			return "", nil, err
		}
	}
	return metricName, metricSelector, nil
}

func newMetricsClient(config *rest.Config, namespace, clientName string) metrics.MetricsClient {
	metricsClient := external_metrics.NewForConfigOrDie(config)
	return metrics.NewMetricsClient(metricsClient, namespace, clientName)
}
