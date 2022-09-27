package api

import (
	"github.com/stretchr/testify/assert"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	"testing"
)

func Test_getMaintainedRatiosCalculationOrder(t *testing.T) {

	tests := []struct {
		name      string
		input     [][2]apiv1.ResourceName
		wantOneOf [][][2]apiv1.ResourceName // in some configuration some items can be swapped and that is fine
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
			input:     [][2]apiv1.ResourceName{{"cpu", "memory"}},
			wantOneOf: [][][2]apiv1.ResourceName{{{"cpu", "memory"}}},
			wantErr:   false,
		},
		{
			name:  "simple",
			input: [][2]apiv1.ResourceName{{"cpu", "memory"}, {"cpu", "storage"}},
			wantOneOf: [][][2]apiv1.ResourceName{
				{{"cpu", "memory"}, {"cpu", "storage"}},
				{{"cpu", "storage"}, {"cpu", "memory"}},
			},
			wantErr: false,
		},
		{
			name:      "cycle 1",
			input:     [][2]apiv1.ResourceName{{"cpu", "cpu"}},
			wantOneOf: nil,
			wantErr:   true,
		},
		{
			name:      "cycle 3",
			input:     [][2]apiv1.ResourceName{{"cpu", "memory"}, {"memory", "storage"}, {"storage", "cpu"}},
			wantOneOf: nil,
			wantErr:   true,
		},
		{
			name:  "2 graphs",
			input: [][2]apiv1.ResourceName{{"cpu", "memory"}, {"storage", "net"}},
			wantOneOf: [][][2]apiv1.ResourceName{
				{{"cpu", "memory"}, {"storage", "net"}},
				{{"storage", "net"}, {"cpu", "memory"}},
			},
			wantErr: false,
		},
		{
			name:  "Same ancestor",
			input: [][2]apiv1.ResourceName{{"cpu", "memory"}, {"cpu", "net"}},
			wantOneOf: [][][2]apiv1.ResourceName{
				{{"cpu", "memory"}, {"cpu", "net"}},
				{{"cpu", "net"}, {"cpu", "memory"}},
			},
			wantErr: false,
		},
		{
			name:      "reorder 3",
			input:     [][2]apiv1.ResourceName{{"cpu", "memory"}, {"memory", "net"}, {"storage", "cpu"}},
			wantOneOf: [][][2]apiv1.ResourceName{{{"storage", "cpu"}, {"cpu", "memory"}, {"memory", "net"}}},
			wantErr:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
		})
	}
}

func Test_applyMaintainRatioVPAPolicy(t *testing.T) {
	tests := []struct {
		name                       string
		recommendation             apiv1.ResourceList
		policy                     *vpa_types.ContainerResourcePolicy
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
			policy: nil,
			containerOriginalResources: apiv1.ResourceList{
				"cpu":    *resource.NewQuantity(1, resource.DecimalSI),
				"memory": *resource.NewQuantity(3000, resource.DecimalSI),
			},
			expectedAnnotations: nil,
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
			policy: &vpa_types.ContainerResourcePolicy{
				MaintainedRatios: [][2]apiv1.ResourceName{{"cpu", "memory"}},
			},
			containerOriginalResources: apiv1.ResourceList{
				"cpu":    *resource.NewQuantity(1, resource.DecimalSI),
				"memory": *resource.NewQuantity(3000, resource.DecimalSI),
			},
			expectedAnnotations: []string{},
			expectedRecommendation: apiv1.ResourceList{
				"cpu":    *resource.NewQuantity(1, resource.DecimalSI),
				"memory": *resource.NewScaledQuantity(3000000, resource.Milli),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			annotations := applyMaintainRatioVPAPolicy(tt.recommendation, tt.policy, tt.containerOriginalResources)
			assert.Equalf(t, annotations, tt.expectedAnnotations, "Expected annotation differs: %v", annotations)
			assert.Equalf(t, tt.recommendation, tt.expectedRecommendation, "Expected recommendation differs: %#v", tt.recommendation)
		})
	}
}
