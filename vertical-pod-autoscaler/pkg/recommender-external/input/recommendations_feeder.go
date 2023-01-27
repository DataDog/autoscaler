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
	for _, vpa := range e.clusterState.Vpas {
		e.externalRecommendationsState.AddOrUpdateVpa(*vpa)
	}
	//FIXME: Delete non-existent VPAs from the model.
}

func (e externalRecommendationsStateFeeder) LoadMetrics() {

	for vpaId, vpa := range e.clusterState.Vpas {
		klog.V(1).Infof("vpa %s %d", vpa.ID.VpaName, vpa.PodCount)

		// We ignore VPAs that don't have any matching pods to reduce the API traffic.
		if vpa.PodCount == 0 {
			continue
		}

		// TODO implement me for real.
		// - Look for matching annotations.
		// TODO add code to extract container name + value
		for key, value := range vpa.Annotations {
			if strings.HasPrefix(key, VpaAnnotationPrefix) {
				klog.V(1).Infof("Found %s:%s on %+v", key, value, vpaId)
			}
		}
		// TODO: do an external metric call to get values
		// TODO: maybe filter on vpa.ResourcePolicy.ContainerPolicies
		e.externalRecommendationsState.AddContainerRecommendation(vpaId, "fake", nil)
		// TODO: use e.metricsClient.GetExternalMetric()
		// TODO: Add events to the VPA in case we don't get a new recommendation.
	}
}

func newMetricsClient(config *rest.Config, namespace, clientName string) metrics.MetricsClient {
	metricsClient := external_metrics.NewForConfigOrDie(config)
	return metrics.NewMetricsClient(metricsClient, namespace, clientName)
}
