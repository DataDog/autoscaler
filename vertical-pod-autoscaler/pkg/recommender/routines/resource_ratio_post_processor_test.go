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
	"testing"

	"github.com/stretchr/testify/assert"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/test"
)

func Test_getMaintainedRatiosCalculationOrder(t *testing.T) {

	tests := []struct {
		name      string
		input     resourceRatioList
		wantOneOf []resourceRatioList // in some configuration some items can be swapped and that is fine
		wantErr   bool
	}{
		{
			name:      "empty",
			input:     nil,
			wantOneOf: nil,
			wantErr:   false,
		},
		{
			name:      "simple",
			input:     resourceRatioList{{original: "cpu", target: "memory"}},
			wantOneOf: []resourceRatioList{{{original: "cpu", target: "memory"}}},
			wantErr:   false,
		},
		{
			name:  "simple",
			input: resourceRatioList{{original: "cpu", target: "memory"}, {original: "cpu", target: "storage"}},
			wantOneOf: []resourceRatioList{
				{{original: "cpu", target: "memory"}, {original: "cpu", target: "storage"}},
				{{original: "cpu", target: "storage"}, {original: "cpu", target: "memory"}},
			},
			wantErr: false,
		},
		{
			name:      "cycle 1",
			input:     resourceRatioList{{original: "cpu", target: "cpu"}},
			wantOneOf: nil,
			wantErr:   true,
		},
		{
			name:      "cycle 3",
			input:     resourceRatioList{{original: "cpu", target: "memory"}, {original: "memory", target: "storage"}, {original: "storage", target: "cpu"}},
			wantOneOf: nil,
			wantErr:   true,
		},
		{
			name:  "2 graphs",
			input: resourceRatioList{{original: "cpu", target: "memory"}, {original: "storage", target: "net"}},
			wantOneOf: []resourceRatioList{
				{{original: "cpu", target: "memory"}, {original: "storage", target: "net"}},
				{{original: "storage", target: "net"}, {original: "cpu", target: "memory"}},
			},
			wantErr: false,
		},
		{
			name:  "Same ancestor",
			input: resourceRatioList{{original: "cpu", target: "memory"}, {original: "cpu", target: "net"}},
			wantOneOf: []resourceRatioList{
				{{original: "cpu", target: "memory"}, {original: "cpu", target: "net"}},
				{{original: "cpu", target: "net"}, {original: "cpu", target: "memory"}},
			},
			wantErr: false,
		},
		{
			name:      "reorder 3",
			input:     resourceRatioList{{original: "cpu", target: "memory"}, {original: "memory", target: "net"}, {original: "storage", target: "cpu"}},
			wantOneOf: []resourceRatioList{{{original: "storage", target: "cpu"}, {original: "cpu", target: "memory"}, {original: "memory", target: "net"}}},
			wantErr:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			{
				got, err := getMaintainedRatiosCalculationOrder(tt.input)
				assert.Equalf(t, tt.wantErr, err != nil, "Error is not the expected one: %v", err)
				if len(tt.wantOneOf) == 0 && len(got) == 0 {
					return
				}
				found := false
				for _, option := range tt.wantOneOf {
					if assert.ObjectsAreEqual(option, got) {
						found = true
						continue
					}
				}
				assert.Truef(t, found, "getMaintainedRatiosCalculationOrder(%v)  =>  %v", tt.input, got)
			}
		})
	}
}

func Test_applyMaintainRatioVPAPolicy(t *testing.T) {
	tests := []struct {
		name                       string
		recommendation             apiv1.ResourceList
		policyRatios               resourceRatioList
		containerOriginalResources apiv1.ResourceList
		expectedAnnotations        []string
		expectedRecommendation     apiv1.ResourceList
	}{
		{
			name: "no Policy",
			recommendation: apiv1.ResourceList{
				"cpu":    *resource.NewQuantity(1, resource.DecimalSI),
				"memory": *resource.NewQuantity(1, resource.DecimalSI),
			},
			policyRatios: nil,
			containerOriginalResources: apiv1.ResourceList{
				"cpu":    *resource.NewQuantity(1, resource.DecimalSI),
				"memory": *resource.NewQuantity(3000, resource.DecimalSI),
			},
			expectedRecommendation: apiv1.ResourceList{
				"cpu":    *resource.NewQuantity(1, resource.DecimalSI),
				"memory": *resource.NewQuantity(1, resource.DecimalSI),
			},
		},
		{
			name: "Policy simple cpu to memory",
			recommendation: apiv1.ResourceList{
				"cpu":    *resource.NewQuantity(1, resource.DecimalSI),
				"memory": *resource.NewQuantity(1, resource.DecimalSI),
			},
			policyRatios: resourceRatioList{{original: "cpu", target: "memory"}},
			containerOriginalResources: apiv1.ResourceList{
				"cpu":    *resource.NewQuantity(1, resource.DecimalSI),
				"memory": *resource.NewQuantity(3000, resource.DecimalSI),
			},
			expectedRecommendation: apiv1.ResourceList{
				"cpu":    *resource.NewQuantity(1, resource.DecimalSI),
				"memory": *resource.NewScaledQuantity(3000000, resource.Milli),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			applyMaintainRatioVPAPolicy(tt.recommendation, tt.policyRatios, tt.containerOriginalResources)
			assert.Equalf(t, tt.recommendation, tt.expectedRecommendation, "Expected recommendation differs: %#v", tt.recommendation)
		})
	}
}

func Test_resourceRatioRecommendationProcessor_Apply(t *testing.T) {
	pod13 := test.Pod().WithName("pod1").AddContainer(test.BuildTestContainer("ctr-name", "1", "3")).Get()

	podRecommendation := &vpa_types.RecommendedPodResources{
		ContainerRecommendations: []vpa_types.RecommendedContainerResources{
			{
				ContainerName: "ctr-name",
				Target: apiv1.ResourceList{
					apiv1.ResourceCPU:    *resource.NewScaledQuantity(5, 0),
					apiv1.ResourceMemory: *resource.NewScaledQuantity(10, 1)},
				LowerBound: apiv1.ResourceList{
					apiv1.ResourceCPU:    *resource.NewScaledQuantity(50, 0),
					apiv1.ResourceMemory: *resource.NewScaledQuantity(100, 1)},
				UpperBound: apiv1.ResourceList{
					apiv1.ResourceCPU:    *resource.NewScaledQuantity(150, 0),
					apiv1.ResourceMemory: *resource.NewScaledQuantity(200, 1)},
			},
		},
	}
	podRecommendationExpected13 := &vpa_types.RecommendedPodResources{
		ContainerRecommendations: []vpa_types.RecommendedContainerResources{
			{
				ContainerName: "ctr-name",
				Target: apiv1.ResourceList{
					apiv1.ResourceCPU:    *resource.NewScaledQuantity(5, 0),
					apiv1.ResourceMemory: *resource.NewScaledQuantity(15000, -3)},
				LowerBound: apiv1.ResourceList{
					apiv1.ResourceCPU:    *resource.NewScaledQuantity(50, 0),
					apiv1.ResourceMemory: *resource.NewScaledQuantity(150000, -3)},
				UpperBound: apiv1.ResourceList{
					apiv1.ResourceCPU:    *resource.NewScaledQuantity(150, 0),
					apiv1.ResourceMemory: *resource.NewScaledQuantity(450000, -3)},
			},
		},
	}

	type args struct {
		podRecommendation *vpa_types.RecommendedPodResources
		ratioPolicies     map[string]resourceRatioList
		conditions        []vpa_types.VerticalPodAutoscalerCondition
		pod               *apiv1.Pod
	}
	tests := []struct {
		name     string
		args     args
		wantReco *vpa_types.RecommendedPodResources
		wantErr  bool
	}{
		{
			name: "nil",
			args: args{
				podRecommendation: nil,
				ratioPolicies:     nil,
				conditions:        nil,
				pod:               pod13,
			},
			wantReco: nil,
			wantErr:  false,
		},
		{
			name: "no policy",
			args: args{
				podRecommendation: podRecommendation,
				ratioPolicies:     nil,
				conditions:        nil,
				pod:               pod13,
			},
			wantReco: podRecommendation,
			wantErr:  false,
		},
		{
			name: "cpu to mem",
			args: args{
				podRecommendation: podRecommendation,
				ratioPolicies:     map[string]resourceRatioList{"ctr-name": resourceRatioList{{original: "cpu", target: "memory"}}},
				pod:               pod13,
			},
			wantReco: podRecommendationExpected13,
			wantErr:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := ResourceRatioRecommendationPostProcessor{}
			got, err := r.apply(tt.args.podRecommendation, tt.args.ratioPolicies, tt.args.pod)
			assert.Equalf(t, tt.wantErr, err != nil, "Error is not the expected one: %v", err)
			assert.Equalf(t, tt.wantReco, got, "Recommendation: Apply(%v, %v, %v, %v)", tt.args.podRecommendation, tt.args.ratioPolicies, tt.args.conditions, tt.args.pod)
		})
	}
}
