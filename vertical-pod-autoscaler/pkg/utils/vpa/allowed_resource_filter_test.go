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
	"github.com/stretchr/testify/assert"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	"testing"
)

func TestAllowedResourceFilter(t *testing.T) {
	tests := []struct {
		name                   string
		allowedResources       []apiv1.ResourceName
		recommendation         vpa_types.RecommendedPodResources
		expectedRecommendation vpa_types.RecommendedPodResources
	}{
		{
			name: "empty allowed resources produces no recommended values",
			recommendation: vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					{
						ContainerName: "container-1",
						Target: apiv1.ResourceList{
							apiv1.ResourceCPU:    resource.MustParse("1500m"),
							apiv1.ResourceMemory: resource.MustParse("64Mi"),
						},
						LowerBound: apiv1.ResourceList{
							apiv1.ResourceCPU:    resource.MustParse("1200m"),
							apiv1.ResourceMemory: resource.MustParse("48Mi"),
						},
						UpperBound: apiv1.ResourceList{
							apiv1.ResourceCPU:    resource.MustParse("1800m"),
							apiv1.ResourceMemory: resource.MustParse("80Mi"),
						},
						UncappedTarget: apiv1.ResourceList{
							apiv1.ResourceCPU:    resource.MustParse("2500m"),
							apiv1.ResourceMemory: resource.MustParse("128Mi"),
						},
					},
					{
						ContainerName: "container-2",
						Target: apiv1.ResourceList{
							apiv1.ResourceCPU:    resource.MustParse("3000m"),
							apiv1.ResourceMemory: resource.MustParse("128Mi"),
						},
						LowerBound: apiv1.ResourceList{
							apiv1.ResourceCPU:    resource.MustParse("2400m"),
							apiv1.ResourceMemory: resource.MustParse("96Mi"),
						},
						UpperBound: apiv1.ResourceList{
							apiv1.ResourceCPU:    resource.MustParse("3600m"),
							apiv1.ResourceMemory: resource.MustParse("160Mi"),
						},
						UncappedTarget: apiv1.ResourceList{
							apiv1.ResourceCPU:    resource.MustParse("50000m"),
							apiv1.ResourceMemory: resource.MustParse("256Mi"),
						},
					},
				},
			},
			expectedRecommendation: vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					{
						ContainerName:  "container-1",
						Target:         apiv1.ResourceList{},
						LowerBound:     apiv1.ResourceList{},
						UpperBound:     apiv1.ResourceList{},
						UncappedTarget: apiv1.ResourceList{},
					},
					{
						ContainerName:  "container-2",
						Target:         apiv1.ResourceList{},
						LowerBound:     apiv1.ResourceList{},
						UpperBound:     apiv1.ResourceList{},
						UncappedTarget: apiv1.ResourceList{},
					},
				},
			},
		},
		{
			name:             "filter partial set of resources",
			allowedResources: []apiv1.ResourceName{apiv1.ResourceCPU, apiv1.ResourceMemory},
			recommendation: vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					{
						ContainerName: "container-1",
						Target: apiv1.ResourceList{
							apiv1.ResourceCPU:                            resource.MustParse("1500m"),
							apiv1.ResourceMemory:                         resource.MustParse("128Mi"),
							apiv1.ResourceEphemeralStorage:               resource.MustParse("10Gi"),
							apiv1.ResourceName("some.extended/resource"): resource.MustParse("4"),
						},
						LowerBound: apiv1.ResourceList{
							apiv1.ResourceCPU:                            resource.MustParse("1200m"),
							apiv1.ResourceMemory:                         resource.MustParse("96Mi"),
							apiv1.ResourceEphemeralStorage:               resource.MustParse("8Gi"),
							apiv1.ResourceName("some.extended/resource"): resource.MustParse("2"),
						},
						UpperBound: apiv1.ResourceList{
							apiv1.ResourceCPU:                            resource.MustParse("1800m"),
							apiv1.ResourceMemory:                         resource.MustParse("256Mi"),
							apiv1.ResourceEphemeralStorage:               resource.MustParse("12Gi"),
							apiv1.ResourceName("some.extended/resource"): resource.MustParse("6"),
						},
						UncappedTarget: apiv1.ResourceList{
							apiv1.ResourceCPU:                            resource.MustParse("2500m"),
							apiv1.ResourceMemory:                         resource.MustParse("1Gi"),
							apiv1.ResourceEphemeralStorage:               resource.MustParse("20Gi"),
							apiv1.ResourceName("some.extended/resource"): resource.MustParse("8"),
						},
					},
				},
			},
			expectedRecommendation: vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					{
						ContainerName: "container-1",
						Target: apiv1.ResourceList{
							apiv1.ResourceCPU:    resource.MustParse("1500m"),
							apiv1.ResourceMemory: resource.MustParse("128Mi"),
						},
						LowerBound: apiv1.ResourceList{
							apiv1.ResourceCPU:    resource.MustParse("1200m"),
							apiv1.ResourceMemory: resource.MustParse("96Mi"),
						},
						UpperBound: apiv1.ResourceList{
							apiv1.ResourceCPU:    resource.MustParse("1800m"),
							apiv1.ResourceMemory: resource.MustParse("256Mi"),
						},
						UncappedTarget: apiv1.ResourceList{
							apiv1.ResourceCPU:    resource.MustParse("2500m"),
							apiv1.ResourceMemory: resource.MustParse("1Gi"),
						},
					},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			processor := NewAllowedResourceFilter(tc.allowedResources)
			processedRecommendation, _, err := processor.Apply(&tc.recommendation, nil, nil, nil)
			assert.NoError(t, err)
			assert.Equal(t, tc.expectedRecommendation, *processedRecommendation)
		})
	}
}
