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

package logic

import (
	"k8s.io/klog/v2"

	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender-external/model"
	upstream_logic "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/logic"
	upstream_model "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
)

// PodResourceRecommenderFactory creates pod resource recommenders
type PodResourceRecommenderFactory interface {
	Make(resources model.ContainerNameToRecommendation) upstream_logic.PodResourceRecommender
}

type podResourceRecommenderFactory struct {
}

func (p podResourceRecommenderFactory) Make(resources model.ContainerNameToRecommendation) upstream_logic.PodResourceRecommender {
	return &podResourceRecommender{containerNameToRecommendedResources: resources}
}

// NewPodResourceRecommenderFactory creates a new pod resource recommender factory.
func NewPodResourceRecommenderFactory() PodResourceRecommenderFactory {
	return &podResourceRecommenderFactory{}
}

type podResourceRecommender struct {
	containerNameToRecommendedResources model.ContainerNameToRecommendation
}

// NewPodResourceRecommender creates a new recommender using containerNameToRecommendedResources to recommend resources.
func NewPodResourceRecommender(containerNameToRecommendedResources model.ContainerNameToRecommendation) upstream_logic.PodResourceRecommender {
	return &podResourceRecommender{
		containerNameToRecommendedResources: containerNameToRecommendedResources,
	}
}

func (r *podResourceRecommender) GetRecommendedPodResources(containerNameToAggregateStateMap upstream_model.ContainerNameToAggregateStateMap) upstream_logic.RecommendedPodResources {
	var recommendation = make(upstream_logic.RecommendedPodResources)
	if len(r.containerNameToRecommendedResources) == 0 {
		klog.V(5).Infof("Returning empty recommendations.")
		return recommendation
	}

	fraction := 1.0 / float64(len(r.containerNameToRecommendedResources))
	minResources := upstream_model.Resources{
		upstream_model.ResourceCPU:    upstream_model.ScaleResource(upstream_model.CPUAmountFromCores(*upstream_logic.PodMinCPUMillicores*0.001), fraction),
		upstream_model.ResourceMemory: upstream_model.ScaleResource(upstream_model.MemoryAmountFromBytes(*upstream_logic.PodMinMemoryMb*1024*1024), fraction),
	}

	for containerName, resources := range r.containerNameToRecommendedResources {
		for resource, resourceAmount := range resources {
			margin := upstream_model.ScaleResource(resourceAmount, *upstream_logic.SafetyMarginFraction)
			resources[resource] += margin
		}

		for resource, resourceAmount := range resources {
			if resourceAmount < minResources[resource] {
				resourceAmount = minResources[resource]
			}
			resources[resource] = resourceAmount
		}

		aggregateStateMap, found := containerNameToAggregateStateMap[containerName]
		if !found {
			klog.Infof("Missing aggregate state of container %s", containerName)
			continue
		}

		recommendation[containerName] = newRecommendedContainerResources(upstream_logic.FilterControlledResources(resources, aggregateStateMap.GetControlledResources()))
	}
	return recommendation
}

func newRecommendedContainerResources(resources upstream_model.Resources) upstream_logic.RecommendedContainerResources {
	return upstream_logic.RecommendedContainerResources{Target: resources, LowerBound: resources, UpperBound: resources}
}
