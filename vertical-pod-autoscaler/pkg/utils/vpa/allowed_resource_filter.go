/*
Copyright 2024 The Kubernetes Authors.

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

package api

import (
	corev1 "k8s.io/api/core/v1"
	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
)

// NewAllowedResourceFilter is a RecommendationProcessor that restricts which resources from a
// VPA's recommendation can actually be applied on a container
func NewAllowedResourceFilter(allowedResources []corev1.ResourceName) RecommendationProcessor {
	return &allowedResourceFilter{
		allowedResources: allowedResources,
	}
}

type allowedResourceFilter struct {
	allowedResources []corev1.ResourceName
}

// Apply chains calls to underlying RecommendationProcessors in order provided on object construction
func (p *allowedResourceFilter) Apply(podRecommendation *vpa_types.RecommendedPodResources,
	policy *vpa_types.PodResourcePolicy,
	conditions []vpa_types.VerticalPodAutoscalerCondition,
	pod *corev1.Pod) (*vpa_types.RecommendedPodResources, ContainerToAnnotationsMap, error) {
	recommendation := podRecommendation
	accumulatedContainerToAnnotationsMap := ContainerToAnnotationsMap{}

	for i, containerRecommendation := range recommendation.ContainerRecommendations {
		recommendation.ContainerRecommendations[i] = filterAllowedContainerResources(containerRecommendation, p.allowedResources)
	}

	return recommendation, accumulatedContainerToAnnotationsMap, nil
}

func filterAllowedContainerResources(recommendation vpa_types.RecommendedContainerResources, allowedResources []corev1.ResourceName) vpa_types.RecommendedContainerResources {
	recommendation.Target = filterResourceList(recommendation.Target, allowedResources)
	recommendation.LowerBound = filterResourceList(recommendation.LowerBound, allowedResources)
	recommendation.UpperBound = filterResourceList(recommendation.UpperBound, allowedResources)
	recommendation.UncappedTarget = filterResourceList(recommendation.UncappedTarget, allowedResources)
	return recommendation
}

func filterResourceList(resourceList corev1.ResourceList, allowedResources []corev1.ResourceName) corev1.ResourceList {
	filteredResourceList := corev1.ResourceList{}
	for resourceName, resourceValue := range resourceList {
		if containsResource(allowedResources, resourceName) {
			filteredResourceList[resourceName] = resourceValue
		}
	}
	return filteredResourceList
}

func containsResource(resourceNames []corev1.ResourceName, target corev1.ResourceName) bool {
	for _, resourceName := range resourceNames {
		if target == resourceName {
			return true
		}
	}
	return false
}
