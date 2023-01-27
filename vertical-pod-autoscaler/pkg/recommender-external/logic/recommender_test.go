/*
Copyright 2023 The Kubernetes Authors.

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
	"testing"

	"github.com/stretchr/testify/assert"

	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender-external/model"
	upstream_logic "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/logic"
	upstream_model "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
)

func TestMinResourcesApplied(t *testing.T) {
	containerNameToRecommendation := model.ContainerNameToRecommendation{
		"container-1": upstream_model.Resources{
			upstream_model.ResourceCPU:    upstream_model.CPUAmountFromCores(0.001),
			upstream_model.ResourceMemory: upstream_model.MemoryAmountFromBytes(1e6),
		},
	}

	recommender := NewPodResourceRecommender(containerNameToRecommendation)

	containerNameToAggregateStateMap := upstream_model.ContainerNameToAggregateStateMap{
		"container-1": &upstream_model.AggregateContainerState{},
	}

	recommendedResources := recommender.GetRecommendedPodResources(containerNameToAggregateStateMap)
	assert.Equal(t, upstream_model.CPUAmountFromCores(*upstream_logic.PodMinCPUMillicores/1000), recommendedResources["container-1"].Target[upstream_model.ResourceCPU])
	assert.Equal(t, upstream_model.MemoryAmountFromBytes(*upstream_logic.PodMinMemoryMb*1024*1024), recommendedResources["container-1"].Target[upstream_model.ResourceMemory])
}

func TestMinResourcesSplitAcrossContainers(t *testing.T) {
	containerNameToRecommendation := model.ContainerNameToRecommendation{
		"container-1": upstream_model.Resources{
			upstream_model.ResourceCPU:    upstream_model.CPUAmountFromCores(0.001),
			upstream_model.ResourceMemory: upstream_model.MemoryAmountFromBytes(1e6),
		},
		"container-2": upstream_model.Resources{
			upstream_model.ResourceCPU:    upstream_model.CPUAmountFromCores(0.001),
			upstream_model.ResourceMemory: upstream_model.MemoryAmountFromBytes(1e6),
		},
	}

	recommender := NewPodResourceRecommender(containerNameToRecommendation)

	containerNameToAggregateStateMap := upstream_model.ContainerNameToAggregateStateMap{
		"container-1": &upstream_model.AggregateContainerState{},
		"container-2": &upstream_model.AggregateContainerState{},
	}

	recommendedResources := recommender.GetRecommendedPodResources(containerNameToAggregateStateMap)
	assert.Equal(t, upstream_model.CPUAmountFromCores((*upstream_logic.PodMinCPUMillicores/1000)/2), recommendedResources["container-1"].Target[upstream_model.ResourceCPU])
	assert.Equal(t, upstream_model.CPUAmountFromCores((*upstream_logic.PodMinCPUMillicores/1000)/2), recommendedResources["container-2"].Target[upstream_model.ResourceCPU])
	assert.Equal(t, upstream_model.MemoryAmountFromBytes((*upstream_logic.PodMinMemoryMb*1024*1024)/2), recommendedResources["container-1"].Target[upstream_model.ResourceMemory])
	assert.Equal(t, upstream_model.MemoryAmountFromBytes((*upstream_logic.PodMinMemoryMb*1024*1024)/2), recommendedResources["container-2"].Target[upstream_model.ResourceMemory])
}

func TestControlledResourcesFiltered(t *testing.T) {
	containerNameToRecommendation := model.ContainerNameToRecommendation{
		"container-1": upstream_model.Resources{
			upstream_model.ResourceCPU:    upstream_model.CPUAmountFromCores(0.001),
			upstream_model.ResourceMemory: upstream_model.MemoryAmountFromBytes(1e6),
		},
	}

	recommender := NewPodResourceRecommender(containerNameToRecommendation)

	containerName := "container-1"
	containerNameToAggregateStateMap := upstream_model.ContainerNameToAggregateStateMap{
		containerName: &upstream_model.AggregateContainerState{
			ControlledResources: &[]upstream_model.ResourceName{upstream_model.ResourceMemory},
		},
	}

	recommendedResources := recommender.GetRecommendedPodResources(containerNameToAggregateStateMap)
	assert.Contains(t, recommendedResources[containerName].Target, upstream_model.ResourceMemory)
	assert.Contains(t, recommendedResources[containerName].LowerBound, upstream_model.ResourceMemory)
	assert.Contains(t, recommendedResources[containerName].UpperBound, upstream_model.ResourceMemory)
	assert.NotContains(t, recommendedResources[containerName].Target, upstream_model.ResourceCPU)
	assert.NotContains(t, recommendedResources[containerName].LowerBound, upstream_model.ResourceCPU)
	assert.NotContains(t, recommendedResources[containerName].UpperBound, upstream_model.ResourceCPU)
}

func TestControlledResourcesFilteredDefault(t *testing.T) {
	containerNameToRecommendation := model.ContainerNameToRecommendation{
		"container-1": upstream_model.Resources{
			upstream_model.ResourceCPU:    upstream_model.CPUAmountFromCores(0.001),
			upstream_model.ResourceMemory: upstream_model.MemoryAmountFromBytes(1e6),
		},
	}

	recommender := NewPodResourceRecommender(containerNameToRecommendation)

	containerName := "container-1"
	containerNameToAggregateStateMap := upstream_model.ContainerNameToAggregateStateMap{
		containerName: &upstream_model.AggregateContainerState{
			ControlledResources: &[]upstream_model.ResourceName{upstream_model.ResourceMemory, upstream_model.ResourceCPU},
		},
	}

	recommendedResources := recommender.GetRecommendedPodResources(containerNameToAggregateStateMap)
	assert.Contains(t, recommendedResources[containerName].Target, upstream_model.ResourceMemory)
	assert.Contains(t, recommendedResources[containerName].LowerBound, upstream_model.ResourceMemory)
	assert.Contains(t, recommendedResources[containerName].UpperBound, upstream_model.ResourceMemory)
	assert.Contains(t, recommendedResources[containerName].Target, upstream_model.ResourceCPU)
	assert.Contains(t, recommendedResources[containerName].LowerBound, upstream_model.ResourceCPU)
	assert.Contains(t, recommendedResources[containerName].UpperBound, upstream_model.ResourceCPU)
}
