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
	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/test"
)

func TestSafetyMarginModifier_Process(t *testing.T) {
	tests := []struct {
		name           string
		vpa            *model.Vpa
		recommendation *vpa_types.RecommendedPodResources
		want           *vpa_types.RecommendedPodResources
	}{
		{
			name: "No containers match",
			vpa: &model.Vpa{Annotations: map[string]string{
				vpaPostProcessorPrefix + "container-other" + vpaPostProcessorSafetyMarginModifierSuffix: "{}",
			}},
			recommendation: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("8.6", "200Mi").GetContainerResources(),
					test.Recommendation().WithContainer("container2").WithTarget("8.2", "300Mi").GetContainerResources(),
				},
			},
			want: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("8.6", "200Mi").GetContainerResources(),
					test.Recommendation().WithContainer("container2").WithTarget("8.2", "300Mi").GetContainerResources(),
				},
			},
		},
		{
			name: "Malformed annotation",
			vpa: &model.Vpa{Annotations: map[string]string{
				vpaPostProcessorPrefix + "container1" + vpaPostProcessorSafetyMarginModifierSuffix: "{\"functio\": \"Linear\"}",
			}},
			recommendation: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1", "200Mi").GetContainerResources(),
				},
			},
			want: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1", "200Mi").GetContainerResources(),
				},
			},
		},
		{
			name: "Invalid Function",
			vpa: &model.Vpa{Annotations: map[string]string{
				vpaPostProcessorPrefix + "container1" + vpaPostProcessorSafetyMarginModifierSuffix: "{\"function\": \"Sqrt\", \"parameters\": [1.2]}",
			}},
			recommendation: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1", "200Mi").GetContainerResources(),
				},
			},
			want: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1", "200Mi").GetContainerResources(),
				},
			},
		},
		{
			name: "2 containers, 1 matching only",
			vpa: &model.Vpa{Annotations: map[string]string{
				vpaPostProcessorPrefix + "container1" + vpaPostProcessorSafetyMarginModifierSuffix: "{\"function\": \"Linear\", \"parameters\": [1.20]}",
			}},
			recommendation: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1", "200Mi").GetContainerResources(),
					test.Recommendation().WithContainer("container2").WithTarget("2", "300Mi").GetContainerResources(),
				},
			},
			want: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1.2", "240Mi").GetContainerResources(),
					test.Recommendation().WithContainer("container2").WithTarget("2", "300Mi").GetContainerResources(),
				},
			},
		},
		{
			name: "2 containers, 2 matching",
			vpa: &model.Vpa{Annotations: map[string]string{
				vpaPostProcessorPrefix + "container1" + vpaPostProcessorSafetyMarginModifierSuffix: "{\"function\": \"Linear\", \"parameters\": [1.20]}",
				vpaPostProcessorPrefix + "container2" + vpaPostProcessorSafetyMarginModifierSuffix: "{\"function\": \"Linear\", \"parameters\": [1.10]}",
			}},
			recommendation: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1", "200Mi").GetContainerResources(),
					test.Recommendation().WithContainer("container2").WithTarget("2", "300Mi").GetContainerResources(),
				},
			},
			want: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1.2", "240Mi").GetContainerResources(),
					test.Recommendation().WithContainer("container2").WithTarget("2.2", "330Mi").GetContainerResources(),
				},
			},
		},
		{
			name: "Case Incensitive",
			vpa: &model.Vpa{Annotations: map[string]string{
				vpaPostProcessorPrefix + "container1" + vpaPostProcessorSafetyMarginModifierSuffix: "{\"function\": \"lIneAR\", \"parameters\": [1.20]}",
			}},
			recommendation: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1", "200Mi").GetContainerResources(),
				},
			},
			want: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1.2", "240Mi").GetContainerResources(),
				},
			},
		},
		{
			name: "Different CPU and Memory modifiers",
			vpa: &model.Vpa{Annotations: map[string]string{
				vpaPostProcessorPrefix + "container1" + vpaPostProcessorSafetyMarginModifierSuffix: "{\"cpu\":{\"function\": \"Linear\", \"parameters\": [1.20]}, \"memory\":{\"function\": \"Linear\", \"parameters\": [1.1]}}",
			}},
			recommendation: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1", "200Mi").GetContainerResources(),
				},
			},
			want: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1.2", "220Mi").GetContainerResources(),
				},
			},
		},
		{
			name: "CPU only modifier",
			vpa: &model.Vpa{Annotations: map[string]string{
				vpaPostProcessorPrefix + "container1" + vpaPostProcessorSafetyMarginModifierSuffix: "{\"cpu\":{\"function\": \"Linear\", \"parameters\": [1.20]}}",
			}},
			recommendation: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1", "200Mi").GetContainerResources(),
				},
			},
			want: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1.2", "200Mi").GetContainerResources(),
				},
			},
		},
		{
			name: "Linear Modifier",
			vpa: &model.Vpa{Annotations: map[string]string{
				vpaPostProcessorPrefix + "container1" + vpaPostProcessorSafetyMarginModifierSuffix: "{\"function\": \"Linear\", \"parameters\": [1.20]}",
			}},
			recommendation: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1", "200Mi").GetContainerResources(),
				},
			},
			want: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1.2", "240Mi").GetContainerResources(),
				},
			},
		},
		{
			name: "Linear Modifier: Wrong number of parameters",
			vpa: &model.Vpa{Annotations: map[string]string{
				vpaPostProcessorPrefix + "container1" + vpaPostProcessorSafetyMarginModifierSuffix: "{\"function\": \"Linear\", \"parameters\": []}",
			}},
			recommendation: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1", "200Mi").GetContainerResources(),
				},
			},
			want: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1", "200Mi").GetContainerResources(),
				},
			},
		},
		{
			name: "Affine Modifier",
			vpa: &model.Vpa{Annotations: map[string]string{
				vpaPostProcessorPrefix + "container1" + vpaPostProcessorSafetyMarginModifierSuffix: "{\"cpu\": {\"function\": \"Affine\", \"parameters\": [100, 1.1]}, \"memory\": {\"function\": \"Affine\", \"parameters\": [104857600, 1.1]}}",
			}},
			recommendation: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1", "200Mi").GetContainerResources(),
				},
			},
			want: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1.2", "320Mi").GetContainerResources(),
				},
			},
		},
		{
			name: "Affine Modifier: Wrong number of parameters",
			vpa: &model.Vpa{Annotations: map[string]string{
				vpaPostProcessorPrefix + "container1" + vpaPostProcessorSafetyMarginModifierSuffix: "{\"function\": \"Affine\", \"parameters\": [100, 1, 2000]}",
			}},
			recommendation: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1", "200Mi").GetContainerResources(),
				},
			},
			want: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1", "200Mi").GetContainerResources(),
				},
			},
		},
		{
			name: "Log Modifier",
			vpa: &model.Vpa{Annotations: map[string]string{
				vpaPostProcessorPrefix + "container1" + vpaPostProcessorSafetyMarginModifierSuffix: "{\"function\": \"Log\", \"parameters\": [100]}",
			}},
			recommendation: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1", "200Mi").GetContainerResources(),
				},
			},
			want: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1.3", "209716032").GetContainerResources(),
				},
			},
		},
		{
			name: "Log Modifier: Wrong number of parameters",
			vpa: &model.Vpa{Annotations: map[string]string{
				vpaPostProcessorPrefix + "container1" + vpaPostProcessorSafetyMarginModifierSuffix: "{\"function\": \"Log\", \"parameters\": [100, 1]}",
			}},
			recommendation: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1", "200Mi").GetContainerResources(),
				},
			},
			want: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1", "200Mi").GetContainerResources(),
				},
			},
		},
		{
			name: "Exponential Modifier",
			vpa: &model.Vpa{Annotations: map[string]string{
				vpaPostProcessorPrefix + "container1" + vpaPostProcessorSafetyMarginModifierSuffix: "{\"function\": \"Exponential\", \"parameters\": [0.5, 10]}",
			}},
			recommendation: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1", "200Mi").GetContainerResources(),
				},
			},
			want: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1316m", "209860015").GetContainerResources(),
				},
			},
		},
		{
			name: "Exponential Modifier: Wrong number of parameters",
			vpa: &model.Vpa{Annotations: map[string]string{
				vpaPostProcessorPrefix + "container1" + vpaPostProcessorSafetyMarginModifierSuffix: "{\"function\": \"Exponential\", \"parameters\": [0.5]}",
			}},
			recommendation: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1", "200Mi").GetContainerResources(),
				},
			},
			want: &vpa_types.RecommendedPodResources{
				ContainerRecommendations: []vpa_types.RecommendedContainerResources{
					test.Recommendation().WithContainer("container1").WithTarget("1", "200Mi").GetContainerResources(),
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := SafetyMarginModifierPostProcessor{DefaultSafetyMarginFactor: 1}
			got := s.Process(tt.vpa, tt.recommendation, nil)
			assert.True(t, equalRecommendedPodResources(tt.want, got), "Process(%v, %v, nil)", tt.vpa, tt.recommendation)
		})
	}
}

func TestSafetyMarginModifier_IgnoreDefaultSafetyMargin(t *testing.T) {
	vpa := &model.Vpa{Annotations: map[string]string{
		vpaPostProcessorPrefix + "container1" + vpaPostProcessorSafetyMarginModifierSuffix: "{\"function\": \"Linear\", \"parameters\": [1.5]}",
	}}
	recommendation := &vpa_types.RecommendedPodResources{
		ContainerRecommendations: []vpa_types.RecommendedContainerResources{
			test.Recommendation().WithContainer("container1").WithTarget("1.2", "240Mi").GetContainerResources(),
		},
	}
	want := &vpa_types.RecommendedPodResources{
		ContainerRecommendations: []vpa_types.RecommendedContainerResources{
			test.Recommendation().WithContainer("container1").WithTarget("1.5", "300Mi").GetContainerResources(),
		},
	}
	t.Run("Ignore default safety margin", func(t *testing.T) {
		s := SafetyMarginModifierPostProcessor{DefaultSafetyMarginFactor: 1.2}
		got := s.Process(vpa, recommendation, nil)
		assert.True(t, equalRecommendedPodResources(want, got), "Process(%v, %v, nil)", vpa, recommendation)
	})
}
