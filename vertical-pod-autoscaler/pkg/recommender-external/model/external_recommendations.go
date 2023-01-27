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

package model

import (
	"time"

	"k8s.io/klog/v2"

	controllerfetcher "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/input/controller_fetcher"
	upstream_model "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
)

const (
	// ObsoleteRecommendationThreshold defines the threshold to consider a recommendation as obsolete.
	ObsoleteRecommendationThreshold = time.Hour * 1
)

// ExternalRecommendationsState holds the external recommendations fetched from external metrics.
// Since we re-use most of ClusterState we need some side structure to keep the information that *we* need.
// When using the external recommender, the recommendations are per-VPA not per pod, so the hierarchy is a bit
// different.
type ExternalRecommendationsState struct {
	// VPA objects in the cluster.
	Vpas map[upstream_model.VpaID]*VpaRecommendationState

	lastGC     time.Time
	gcInterval time.Duration
}

// Size is the number of VPAs being tracked by the VPA
func (s *ExternalRecommendationsState) Size() int {
	return len(s.Vpas)
}

// VpaRecommendationState holds recommendations information about a VPA.
type VpaRecommendationState struct {
	// Unique id of the VPA.
	ID upstream_model.VpaID
	// Containers known to exist for this set of pods and their associated recommendations.
	Containers map[string]*ContainerRecommendationState
	// Created is kept to know if we are dealing with the same object
	Created time.Time
}

// ContainerRecommendationState holds recommendations about a single container managed by a VPA.
type ContainerRecommendationState struct {
	// The recommended resources.
	RawRecommendation upstream_model.Resources
	// Time as which the external recommendation was generated.
	time time.Time
}

// ContainerNameToRecommendation maps a container name to raw recommended resources from the external metrics.
type ContainerNameToRecommendation map[string]upstream_model.Resources

// NewExternalRecommendationsState returns a new ExternalRecommendations
func NewExternalRecommendationsState(gcInterval time.Duration) *ExternalRecommendationsState {
	return &ExternalRecommendationsState{
		Vpas:       make(map[upstream_model.VpaID]*VpaRecommendationState),
		lastGC:     time.Unix(0, 0),
		gcInterval: gcInterval,
	}
}

// AddContainerRecommendation tracks a container recommendation.
func (s *ExternalRecommendationsState) AddContainerRecommendation(vpaId upstream_model.VpaID, containerName string, recommendation upstream_model.Resources) error {
	vpa, vpaExists := s.Vpas[vpaId]
	if !vpaExists {
		return upstream_model.NewKeyError(vpaId)
	}
	if container, containerExists := vpa.Containers[containerName]; !containerExists {
		vpa.Containers[containerName] = NewContainerRecommendationState(recommendation)
	} else {
		// Container aleady exists. Possibly update the request.
		container.RawRecommendation = recommendation
		container.time = time.Now()
	}
	return nil
}

// NewVpaRecommendationState creates a new VPA recommendation state.
func NewVpaRecommendationState(id upstream_model.VpaID, created time.Time) *VpaRecommendationState {
	return &VpaRecommendationState{
		ID:         id,
		Containers: make(map[string]*ContainerRecommendationState),
		Created:    created,
	}
}

// NewContainerRecommendationState creates a new container recommendation state.
func NewContainerRecommendationState(recommendation upstream_model.Resources) *ContainerRecommendationState {
	return &ContainerRecommendationState{RawRecommendation: recommendation, time: time.Now()}
}

// AddOrUpdateVpa adds a new VPA with a given ID to the ClusterState if it
// didn't yet exist. If the VPA already existed but had a different pod
// selector, the pod selector is updated. Updates the links between the VPA and
// all aggregations it matches.
func (s *ExternalRecommendationsState) AddOrUpdateVpa(vpa upstream_model.Vpa) error {
	vpaReco, vpaExists := s.Vpas[vpa.ID]
	if vpaExists {
		if vpaReco.Created != vpa.Created {
			s.DeleteVpa(vpa.ID)
		}
		vpaExists = false
	}
	if !vpaExists {
		vpaReco = NewVpaRecommendationState(vpa.ID, vpa.Created)
		s.Vpas[vpa.ID] = vpaReco
	}
	return nil
}

// DeleteVpa removes a VPA with the given ID from the ClusterState.
func (s *ExternalRecommendationsState) DeleteVpa(vpaID upstream_model.VpaID) error {
	_, vpaExists := s.Vpas[vpaID]
	if !vpaExists {
		return upstream_model.NewKeyError(vpaID)
	}
	delete(s.Vpas, vpaID)
	return nil
}

// GarbageCollect removes obsolete states from the ExternalRecommendationsState.
// Recommendations are obsolete if they are older than the GC threshold.
func (s *ExternalRecommendationsState) GarbageCollect(now time.Time, controllerFetcher controllerfetcher.ControllerFetcher) {
	klog.V(1).Info("Garbage collection of GarbageCollect triggered")

	for vpaId, vpaState := range s.Vpas {
		for container, containerState := range vpaState.Containers {
			if now.Sub(containerState.time) > ObsoleteRecommendationThreshold {
				klog.V(1).Infof("Removing expired VpaRecommendationState for %+v:%s", vpaId, container)
				delete(vpaState.Containers, container)
			}
		}

		// If the VPA is very new, keep it.
		if now.Sub(vpaState.Created) < ObsoleteRecommendationThreshold {
			continue
		}
		if len(vpaState.Containers) == 0 {
			klog.V(1).Infof("Removing expired VpaRecommendationState for %+v", vpaState)
			s.DeleteVpa(vpaId)
		}
	}
}

// RateLimitedGarbageCollect removes obsolete state.
func (s *ExternalRecommendationsState) RateLimitedGarbageCollect(now time.Time, controllerFetcher controllerfetcher.ControllerFetcher) {
	if now.Sub(s.lastGC) < s.gcInterval {
		return
	}
	s.GarbageCollect(now, controllerFetcher)
	s.lastGC = now
}
