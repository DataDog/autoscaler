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
	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender-external/model"
	upstream_model "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"

	api_utils "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/vpa"
)

// GetContainerNameToRecommendedResources returns a map of container to recommended resources.
// It will filter out all the containers that do not have any matching resource policy.
func GetContainerNameToRecommendedResources(vpa *upstream_model.Vpa, vpaRecommendations *model.VpaRecommendationState) model.ContainerNameToRecommendation {
	filteredContainerNameToRecommendation := make(model.ContainerNameToRecommendation)

	for containerName, state := range vpaRecommendations.Containers {
		containerResourcePolicy := api_utils.GetContainerResourcePolicy(containerName, vpa.ResourcePolicy)
		autoscalingDisabled := containerResourcePolicy != nil && containerResourcePolicy.Mode != nil &&
			*containerResourcePolicy.Mode == vpa_types.ContainerScalingModeOff

		if !autoscalingDisabled && len(state.RawRecommendation) != 0 {
			filteredContainerNameToRecommendation[containerName] = state.RawRecommendation
		}
	}
	return filteredContainerNameToRecommendation
}

// GetContainerNameToAggregateStateMap returns ContainerNameToAggregateStateMap for pods.
// Same as upstream method without the `aggregatedContainerState.TotalSamplesCount > 0` check.
func GetContainerNameToAggregateStateMap(vpa *upstream_model.Vpa) upstream_model.ContainerNameToAggregateStateMap {
	containerNameToAggregateStateMap := vpa.AggregateStateByContainerName()
	filteredContainerNameToAggregateStateMap := make(upstream_model.ContainerNameToAggregateStateMap)

	for containerName, aggregatedContainerState := range containerNameToAggregateStateMap {
		containerResourcePolicy := api_utils.GetContainerResourcePolicy(containerName, vpa.ResourcePolicy)
		autoscalingDisabled := containerResourcePolicy != nil && containerResourcePolicy.Mode != nil &&
			*containerResourcePolicy.Mode == vpa_types.ContainerScalingModeOff
		if !autoscalingDisabled {
			aggregatedContainerState.UpdateFromPolicy(containerResourcePolicy)
			filteredContainerNameToAggregateStateMap[containerName] = aggregatedContainerState
		}
	}
	return filteredContainerNameToAggregateStateMap
}
