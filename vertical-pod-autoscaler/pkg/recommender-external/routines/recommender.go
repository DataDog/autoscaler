/*
Copyright 2017 The Kubernetes Authors.

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

package routines

import (
	"context"
	"time"

	"k8s.io/client-go/informers"
	kube_client "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	vpa_clientset "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned"
	vpa_api "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned/typed/autoscaling.k8s.io/v1"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender-external/input"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender-external/logic"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender-external/model"
	upstream_input "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/input"
	controllerfetcher "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/input/controller_fetcher"
	upstream_logic "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/logic"
	upstream_model "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
	upstream_routines "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/routines"
	metrics_recommender "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/metrics/recommender"
	vpa_utils "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/vpa"
)

// DefaultRecommenderName is the default recommender name.
const DefaultRecommenderName = "recommender-external"

const (
	// AggregateContainerStateGCInterval defines how often expired AggregateContainerStates are garbage collected.
	AggregateContainerStateGCInterval         = 1 * time.Hour
	scaleCacheEntryLifetime                   = time.Hour
	scaleCacheEntryFreshnessTime              = 10 * time.Minute
	scaleCacheEntryJitterFactor       float64 = 1.
	defaultResyncPeriod                       = 10 * time.Minute
)

type recommender struct {
	clusterState                       *upstream_model.ClusterState
	clusterStateFeeder                 upstream_input.ClusterStateFeeder
	controllerFetcher                  controllerfetcher.ControllerFetcher
	vpaClient                          vpa_api.VerticalPodAutoscalersGetter
	podResourceRecommenderFactory      logic.PodResourceRecommenderFactory
	recommendationPostProcessor        []upstream_routines.RecommendationPostProcessor
	externalRecommendationsState       *model.ExternalRecommendationsState
	externalRecommendationsStateFeeder input.ExternalRecommendationsStateFeeder
}

func (r *recommender) MaintainCheckpoints(ctx context.Context, minCheckpoints int) {
	// We don't use checkpoints
}

func (r *recommender) GetClusterState() *upstream_model.ClusterState {
	return r.clusterState
}

func (r *recommender) GetClusterStateFeeder() upstream_input.ClusterStateFeeder {
	return r.clusterStateFeeder
}

func (r *recommender) GetRecommendationState() *model.ExternalRecommendationsState {
	return r.externalRecommendationsState
}

func (r *recommender) GetRecommendationStateFeeder() input.ExternalRecommendationsStateFeeder {
	return r.externalRecommendationsStateFeeder
}

// UpdateVPAs Updates VPA CRD objects' statuses.
// This is a copy of the regular recommender UpdateVPAs except the line with podResourceRecommenderFactory,
// TODO: we should try to make this hookable upstream. Or we could find a way to have the VpaID passed to the recommender.
func (r *recommender) UpdateVPAs() {
	cnt := metrics_recommender.NewObjectCounter()
	defer cnt.Observe()

	for _, observedVpa := range r.clusterState.ObservedVpas {
		key := upstream_model.VpaID{
			Namespace: observedVpa.Namespace,
			VpaName:   observedVpa.Name,
		}

		klog.V(5).Infof("Observing %+v", key)

		vpa, found := r.clusterState.Vpas[key]
		if !found {
			klog.Infof("We do not know this VPA yet: %+v", key)
			continue
		}

		vpaRecommendation, found := r.externalRecommendationsState.Vpas[key]
		if !found {
			klog.Infof("We do not have recommendations for this VPA yet: %+v", key)
			continue
		}

		klog.V(5).Infof("Container(s) found for vpa %+v: %q", key, vpaRecommendation.Containers)

		resources := r.podResourceRecommenderFactory.
			Make(GetContainerNameToRecommendedResources(vpa, vpaRecommendation)).
			GetRecommendedPodResources(GetContainerNameToAggregateStateMap(vpa))
		had := vpa.HasRecommendation()

		klog.V(5).Infof("Found recommendation for %+v: %+v (had: %v)", key, resources, had)

		listOfResourceRecommendation := upstream_logic.MapToListOfRecommendedContainerResources(resources)

		for _, postProcessor := range r.recommendationPostProcessor {
			listOfResourceRecommendation = postProcessor.Process(vpa, listOfResourceRecommendation, observedVpa.Spec.ResourcePolicy)
		}

		klog.V(5).Infof("Updating %+v with recommendation %+v", key, listOfResourceRecommendation)

		vpa.UpdateRecommendation(listOfResourceRecommendation)
		if vpa.HasRecommendation() && !had {
			metrics_recommender.ObserveRecommendationLatency(vpa.Created)
		}
		hasMatchingPods := vpa.PodCount > 0
		vpa.UpdateConditions(hasMatchingPods)
		if err := r.clusterState.RecordRecommendation(vpa, time.Now()); err != nil {
			klog.Warningf("%v", err)
			if klog.V(4).Enabled() {
				klog.Infof("VPA dump")
				klog.Infof("%+v", vpa)
				klog.Infof("HasMatchingPods: %v", hasMatchingPods)
				klog.Infof("PodCount: %v", vpa.PodCount)
				pods := r.clusterState.GetMatchingPods(vpa)
				klog.Infof("MatchingPods: %+v", pods)
				if len(pods) != vpa.PodCount {
					klog.Errorf("ClusterState pod count and matching pods disagree for vpa %v/%v", vpa.ID.Namespace, vpa.ID.VpaName)
				}
			}
		}
		cnt.Add(vpa)

		_, err := vpa_utils.UpdateVpaStatusIfNeeded(
			r.vpaClient.VerticalPodAutoscalers(vpa.ID.Namespace), vpa.ID.VpaName, vpa.AsStatus(), &observedVpa.Status)
		if err != nil {
			klog.Errorf(
				"Cannot update VPA %v object. Reason: %+v", vpa.ID.VpaName, err)
		}
	}
}

func (r *recommender) RunOnce() {
	timer := metrics_recommender.NewExecutionTimer()
	defer timer.ObserveTotal()

	klog.V(3).Infof("ExternalRecommender Run")

	r.clusterStateFeeder.LoadVPAs()
	r.externalRecommendationsStateFeeder.LoadVPAs()
	timer.ObserveStep("LoadVPAs")

	// Needed to see if VPAs have matching pods.
	r.clusterStateFeeder.LoadPods()
	timer.ObserveStep("LoadPods")

	r.clusterStateFeeder.ObserveOOMs(r.ObserverOOM)
	timer.ObserveStep("RecordOOMs")

	r.externalRecommendationsStateFeeder.LoadMetrics()
	timer.ObserveStep("LoadMetrics")

	r.UpdateVPAs()
	timer.ObserveStep("UpdateVPAs")

	klog.V(3).Infof("ClusterState is tracking %d - %d aggregated container states", r.clusterState.StateMapSize(), r.externalRecommendationsState.Size())
}

func (r *recommender) ObserverOOM(containerID upstream_model.ContainerID, timestamp time.Time, requestedMemory upstream_model.ResourceAmount) error {
	klog.V(3).Infof("Observed OOM on %s at %s with memory %d", containerID, timestamp, requestedMemory)
	return nil
}

// RecommenderFactory makes instances of ExternalRecommender.
type RecommenderFactory struct {
	ClusterState            *upstream_model.ClusterState
	ExternalRecommendations *model.ExternalRecommendationsState

	ClusterStateFeeder                 upstream_input.ClusterStateFeeder
	ControllerFetcher                  controllerfetcher.ControllerFetcher
	PodResourceRecommenderFactory      logic.PodResourceRecommenderFactory
	VpaClient                          vpa_api.VerticalPodAutoscalersGetter
	RecommendationPostProcessors       []upstream_routines.RecommendationPostProcessor
	ExternalRecommendationsState       *model.ExternalRecommendationsState
	ExternalRecommendationsStateFeeder input.ExternalRecommendationsStateFeeder
}

// Make creates a new recommender instance,
// which can be run in order to provide continuous resource recommendations for containers.
func (c RecommenderFactory) Make() upstream_routines.Recommender {
	recommender := &recommender{
		clusterState:                       c.ClusterState,
		clusterStateFeeder:                 c.ClusterStateFeeder,
		controllerFetcher:                  c.ControllerFetcher,
		vpaClient:                          c.VpaClient,
		podResourceRecommenderFactory:      c.PodResourceRecommenderFactory,
		recommendationPostProcessor:        c.RecommendationPostProcessors,
		externalRecommendationsState:       c.ExternalRecommendationsState,
		externalRecommendationsStateFeeder: c.ExternalRecommendationsStateFeeder,
	}
	klog.V(3).Infof("New ExternalRecommender created %+v", recommender)
	return recommender
}

// NewExternalRecommender creates a new recommender instance.
// Dependencies are created automatically.
// Deprecated; use RecommenderFactory instead.
func NewExternalRecommender(config *rest.Config, namespace string, recommenderName string, recommendationPostProcessors []upstream_routines.RecommendationPostProcessor) upstream_routines.Recommender {
	// We re-use ClusterState and ClusterStateFeeder from the original recommender to avoid duplicating too much code.
	// We might re-implement them leader.

	clusterState := upstream_model.NewClusterState(AggregateContainerStateGCInterval)
	kubeClient := kube_client.NewForConfigOrDie(config)
	factory := informers.NewSharedInformerFactoryWithOptions(kubeClient, defaultResyncPeriod, informers.WithNamespace(namespace))
	controllerFetcher := controllerfetcher.NewControllerFetcher(config, kubeClient, factory, scaleCacheEntryFreshnessTime, scaleCacheEntryLifetime, scaleCacheEntryJitterFactor)

	externalRecommendationState := model.NewExternalRecommendationsState(AggregateContainerStateGCInterval)

	return RecommenderFactory{
		ClusterState:                       clusterState,
		ClusterStateFeeder:                 upstream_input.NewClusterStateFeeder(config, clusterState, true, namespace, "default-metrics-client", recommenderName),
		ControllerFetcher:                  controllerFetcher,
		ExternalRecommendationsState:       externalRecommendationState,
		ExternalRecommendationsStateFeeder: input.NewExternalRecommendationsStateFeeder(config, clusterState, externalRecommendationState, namespace, "external-metrics-client"),
		VpaClient:                          vpa_clientset.NewForConfigOrDie(config).AutoscalingV1(),
		PodResourceRecommenderFactory:      logic.NewPodResourceRecommenderFactory(),
		RecommendationPostProcessors:       recommendationPostProcessors,
	}.Make()
}
