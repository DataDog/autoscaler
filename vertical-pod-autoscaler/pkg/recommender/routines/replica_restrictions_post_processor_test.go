/*
Copyright 2022 The Kubernetes Authors.

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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/test"
)

func Test_isDownscaleIsUpscale(t *testing.T) {
	type args struct {
		recommendation             apiv1.ResourceList
		containerOriginalResources apiv1.ResourceList
	}
	tests := []struct {
		name        string
		args        args
		isDownscale bool
		isUpscale   bool
	}{
		{
			name: "same",
			args: args{
				recommendation: apiv1.ResourceList{
					apiv1.ResourceCPU:    *resource.NewScaledQuantity(5, 0),
					apiv1.ResourceMemory: *resource.NewScaledQuantity(15000, -3)},
				containerOriginalResources: apiv1.ResourceList{
					apiv1.ResourceCPU:    *resource.NewScaledQuantity(5, 0),
					apiv1.ResourceMemory: *resource.NewScaledQuantity(15000, -3)},
			},
			isDownscale: false,
			isUpscale:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.isDownscale, isDownscale(tt.args.recommendation, tt.args.containerOriginalResources), "isDownscale(%v, %v)", tt.args.recommendation, tt.args.containerOriginalResources)
			assert.Equalf(t, tt.isUpscale, isUpscale(tt.args.recommendation, tt.args.containerOriginalResources), "isUpscale(%v, %v)", tt.args.recommendation, tt.args.containerOriginalResources)
		})
	}
}

func Test_isRestrictedScaling(t *testing.T) {
	type args struct {
		containerRecommendation     *vpa_types.RecommendedContainerResources
		container                   apiv1.Container
		downscaleIfLessReplicasThan int
		upscaleIfMoreReplicasThan   int
		replicas                    int
	}
	tests := []struct {
		name         string
		args         args
		isRestricted bool
	}{
		{
			name: "not restricted",
			args: args{
				containerRecommendation: &vpa_types.RecommendedContainerResources{
					ContainerName: "ctr-name",
					Target:        apiv1.ResourceList{apiv1.ResourceCPU: *resource.NewScaledQuantity(5, 0)},
				},
				container: apiv1.Container{
					Name: "ctr-name",
					Resources: apiv1.ResourceRequirements{
						Requests: apiv1.ResourceList{apiv1.ResourceCPU: *resource.NewScaledQuantity(5, 0)},
					},
				},
				downscaleIfLessReplicasThan: 10,
				upscaleIfMoreReplicasThan:   100,
				replicas:                    1,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.isRestricted, isRestrictedScaling(tt.args.containerRecommendation, tt.args.container, tt.args.downscaleIfLessReplicasThan, tt.args.upscaleIfMoreReplicasThan, tt.args.replicas), "isRestrictedScaling(%v, %v, %v, %v, %v)", tt.args.containerRecommendation, tt.args.container, tt.args.downscaleIfLessReplicasThan, tt.args.upscaleIfMoreReplicasThan, tt.args.replicas)
		})
	}
}
func TestReplicaRestrictionsPostProcessor_apply(t *testing.T) {
	pod53 := test.Pod().WithName("pod1").
		AddContainer(test.BuildTestContainer("ctr-name", "5", "3")).
		Get()

	cpu, err := resource.ParseQuantity("5")
	assert.NoError(t, err)
	mem, err := resource.ParseQuantity("3")
	assert.NoError(t, err)

	resourcesList53 := apiv1.ResourceList{
		apiv1.ResourceCPU:    cpu,
		apiv1.ResourceMemory: mem,
	}

	podCurrentResources := &vpa_types.RecommendedPodResources{
		ContainerRecommendations: []vpa_types.RecommendedContainerResources{
			{
				ContainerName: "ctr-name",
				Target:        resourcesList53,
				LowerBound:    resourcesList53,
				UpperBound:    resourcesList53,
			},
		},
	}
	// A recommendation to downscale
	podRecommendationDownscale := &vpa_types.RecommendedPodResources{
		ContainerRecommendations: []vpa_types.RecommendedContainerResources{
			{
				ContainerName: "ctr-name",
				Target: apiv1.ResourceList{
					apiv1.ResourceCPU:    *resource.NewScaledQuantity(2, 0),
					apiv1.ResourceMemory: *resource.NewScaledQuantity(3, 0)},
				LowerBound: apiv1.ResourceList{
					apiv1.ResourceCPU:    *resource.NewScaledQuantity(2, 0),
					apiv1.ResourceMemory: *resource.NewScaledQuantity(3, 0)},
				UpperBound: apiv1.ResourceList{
					apiv1.ResourceCPU:    *resource.NewScaledQuantity(2, 0),
					apiv1.ResourceMemory: *resource.NewScaledQuantity(3, 0)},
			},
		},
	}
	// A recommendation to upscale
	podRecommendationUpscale := &vpa_types.RecommendedPodResources{
		ContainerRecommendations: []vpa_types.RecommendedContainerResources{
			{
				ContainerName: "ctr-name",
				Target: apiv1.ResourceList{
					apiv1.ResourceCPU:    *resource.NewScaledQuantity(10, 0),
					apiv1.ResourceMemory: *resource.NewScaledQuantity(3, 0)},
				LowerBound: apiv1.ResourceList{
					apiv1.ResourceCPU:    *resource.NewScaledQuantity(10, 0),
					apiv1.ResourceMemory: *resource.NewScaledQuantity(3, 0)},
				UpperBound: apiv1.ResourceList{
					apiv1.ResourceCPU:    *resource.NewScaledQuantity(150, 0),
					apiv1.ResourceMemory: *resource.NewScaledQuantity(3, 0)},
			},
		},
	}

	type args struct {
		podRecommendation           *vpa_types.RecommendedPodResources
		replicas                    int
		downscaleIfLessReplicasThan int
		upscaleIfMoreReplicasThan   int
		pod                         *apiv1.Pod
	}
	tests := []struct {
		name    string
		args    args
		want    *vpa_types.RecommendedPodResources
		wantErr assert.ErrorAssertionFunc
	}{
		{
			// The replica count is bellow the watermark, so we can downscale.
			name: "downscale allowed",
			args: args{
				podRecommendation:           podRecommendationDownscale,
				replicas:                    5,
				downscaleIfLessReplicasThan: 10,
				upscaleIfMoreReplicasThan:   100,
				pod:                         pod53,
			},
			want:    podRecommendationDownscale,
			wantErr: assert.NoError,
		},
		{
			// The replica count is above the watermark, so we can upscale.
			name: "upscale allowed",
			args: args{
				podRecommendation:           podRecommendationUpscale,
				replicas:                    105,
				downscaleIfLessReplicasThan: 10,
				upscaleIfMoreReplicasThan:   100,
				pod:                         pod53,
			},
			want:    podRecommendationUpscale,
			wantErr: assert.NoError,
		},
		{
			// With replicas between low and high, we should not do anything.
			name: "downscale restricted",
			args: args{
				podRecommendation:           podRecommendationDownscale,
				replicas:                    50,
				downscaleIfLessReplicasThan: 10,
				upscaleIfMoreReplicasThan:   100,
				pod:                         pod53,
			},
			want:    podCurrentResources,
			wantErr: assert.NoError,
		},
		{
			// With replicas between low and high, we should not do anything.
			name: "upscale restricted",
			args: args{
				podRecommendation:           podRecommendationUpscale,
				replicas:                    50,
				downscaleIfLessReplicasThan: 10,
				upscaleIfMoreReplicasThan:   100,
				pod:                         pod53,
			},
			want:    podCurrentResources,
			wantErr: assert.NoError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &ReplicaRestrictionsPostProcessor{}
			got, err := r.apply(tt.args.podRecommendation, tt.args.replicas, tt.args.downscaleIfLessReplicasThan, tt.args.upscaleIfMoreReplicasThan, tt.args.pod)
			if !tt.wantErr(t, err, fmt.Sprintf("apply(%v, %v, %v, %v, %v)", tt.args.podRecommendation, tt.args.replicas, tt.args.downscaleIfLessReplicasThan, tt.args.upscaleIfMoreReplicasThan, tt.args.pod)) {
				return
			}
			assert.Equalf(t, tt.want.ContainerRecommendations[0].Target, got.ContainerRecommendations[0].Target, "apply(%v, %v, %v, %v, %v)", tt.args.podRecommendation, tt.args.replicas, tt.args.downscaleIfLessReplicasThan, tt.args.upscaleIfMoreReplicasThan, tt.args.pod)
		})
	}
}
