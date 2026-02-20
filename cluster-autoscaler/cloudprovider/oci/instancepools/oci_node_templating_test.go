package instancepools

import (
	"testing"

	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider/oci/instancepools/consts"
)

func TestExtractNodeTemplateAttributes(t *testing.T) {
	ns := consts.DefaultOciNodeTemplateTagNamespace

	tests := []struct {
		name          string
		freeformTags  map[string]string
		definedTags   map[string]map[string]string
		wantLabels    map[string]string
		wantTaints    []apiv1.Taint
		wantResources map[string]string // string quantities for easy comparison
		wantOptions   map[string]string
	}{
		// --- Labels ---
		{
			name: "label from freeform tag",
			freeformTags: map[string]string{
				"cluster-autoscaler/node-template/label/app": "nginx",
			},
			wantLabels: map[string]string{"app": "nginx"},
		},
		{
			name: "label from defined tag",
			definedTags: map[string]map[string]string{
				ns: {"cluster-autoscaler/node-template/label/environment": "prod"},
			},
			wantLabels: map[string]string{"environment": "prod"},
		},
		{
			name: "label with period substitution",
			freeformTags: map[string]string{
				"cluster-autoscaler/node-template/label/app~2kubernetes~2io/name": "my-app",
			},
			wantLabels: map[string]string{"app.kubernetes.io/name": "my-app"},
		},
		{
			name: "freeform label overrides defined label",
			freeformTags: map[string]string{
				"cluster-autoscaler/node-template/label/app": "override",
			},
			definedTags: map[string]map[string]string{
				ns: {"cluster-autoscaler/node-template/label/app": "original"},
			},
			wantLabels: map[string]string{"app": "override"},
		},
		{
			name: "labels from both freeform and defined tags",
			freeformTags: map[string]string{
				"cluster-autoscaler/node-template/label/app": "nginx",
			},
			definedTags: map[string]map[string]string{
				ns: {"cluster-autoscaler/node-template/label/environment": "prod"},
			},
			wantLabels: map[string]string{"app": "nginx", "environment": "prod"},
		},
		{
			name: "labels in wrong namespace are ignored",
			definedTags: map[string]map[string]string{
				"OtherNamespace": {"cluster-autoscaler/node-template/label/app": "nginx"},
			},
		},
		// --- Taints ---
		{
			name: "taint from freeform tag",
			freeformTags: map[string]string{
				"cluster-autoscaler/node-template/taint/dedicated": "gpu:NoSchedule",
			},
			wantTaints: []apiv1.Taint{{Key: "dedicated", Value: "gpu", Effect: apiv1.TaintEffectNoSchedule}},
		},
		{
			name: "taint from defined tag",
			definedTags: map[string]map[string]string{
				ns: {"cluster-autoscaler/node-template/taint/dedicated": "gpu:NoSchedule"},
			},
			wantLabels: map[string]string{},
			wantTaints: []apiv1.Taint{{Key: "dedicated", Value: "gpu", Effect: apiv1.TaintEffectNoSchedule}},
		},
		{
			name: "taint with period substitution in key",
			freeformTags: map[string]string{
				"cluster-autoscaler/node-template/taint/nvidia~2com/gpu": "present:NoSchedule",
			},
			wantTaints: []apiv1.Taint{{Key: "nvidia.com/gpu", Value: "present", Effect: apiv1.TaintEffectNoSchedule}},
		},
		{
			name: "taint with empty value",
			freeformTags: map[string]string{
				"cluster-autoscaler/node-template/taint/gpu": ":NoSchedule",
			},
			wantTaints: []apiv1.Taint{{Key: "gpu", Value: "", Effect: apiv1.TaintEffectNoSchedule}},
		},
		{
			name: "all three taint effects",
			freeformTags: map[string]string{
				"cluster-autoscaler/node-template/taint/a": "v:NoSchedule",
				"cluster-autoscaler/node-template/taint/b": "v:NoExecute",
				"cluster-autoscaler/node-template/taint/c": "v:PreferNoSchedule",
			},
			wantTaints: []apiv1.Taint{
				{Key: "a", Value: "v", Effect: apiv1.TaintEffectNoSchedule},
				{Key: "b", Value: "v", Effect: apiv1.TaintEffectNoExecute},
				{Key: "c", Value: "v", Effect: apiv1.TaintEffectPreferNoSchedule},
			},
		},
		{
			name: "taints with invalid effects are ignored",
			freeformTags: map[string]string{
				"cluster-autoscaler/node-template/taint/valid":          "v:NoSchedule",
				"cluster-autoscaler/node-template/taint/no-effect":      "v",
				"cluster-autoscaler/node-template/taint/invalid-effect": "v:BadEffect",
			},
			wantTaints: []apiv1.Taint{{Key: "valid", Value: "v", Effect: apiv1.TaintEffectNoSchedule}},
		},
		{
			name: "taints from both freeform and defined tags",
			freeformTags: map[string]string{
				"cluster-autoscaler/node-template/taint/spot": "true:PreferNoSchedule",
			},
			definedTags: map[string]map[string]string{
				ns: {"cluster-autoscaler/node-template/taint/gpu": "required:NoSchedule"},
			},
			wantTaints: []apiv1.Taint{
				{Key: "spot", Value: "true", Effect: apiv1.TaintEffectPreferNoSchedule},
				{Key: "gpu", Value: "required", Effect: apiv1.TaintEffectNoSchedule},
			},
		},
		// --- Resources ---
		{
			name: "resource from freeform tag",
			freeformTags: map[string]string{
				"cluster-autoscaler/node-template/resources/nvidia~2com/gpu": "2",
			},
			wantResources: map[string]string{"nvidia.com/gpu": "2"},
		},
		{
			name: "resource from defined tag",
			definedTags: map[string]map[string]string{
				ns: {"cluster-autoscaler/node-template/resources/nvidia~2com/gpu": "4"},
			},
			wantResources: map[string]string{"nvidia.com/gpu": "4"},
		},
		{
			name: "freeform resource overrides defined resource",
			freeformTags: map[string]string{
				"cluster-autoscaler/node-template/resources/nvidia~2com/gpu": "4",
			},
			definedTags: map[string]map[string]string{
				ns: {"cluster-autoscaler/node-template/resources/nvidia~2com/gpu": "2"},
			},
			wantResources: map[string]string{"nvidia.com/gpu": "4"},
		},
		{
			name: "invalid resource quantity is skipped",
			freeformTags: map[string]string{
				"cluster-autoscaler/node-template/resources/valid":   "100Gi",
				"cluster-autoscaler/node-template/resources/invalid": "not-a-quantity",
			},
			wantResources: map[string]string{"valid": "100Gi"},
		},
		{
			name: "resources in wrong namespace are ignored",
			definedTags: map[string]map[string]string{
				"OtherNamespace": {"cluster-autoscaler/node-template/resources/nvidia~2com/gpu": "2"},
			},
		},
		// --- Autoscaling options ---
		{
			name: "autoscaling option from freeform tag",
			freeformTags: map[string]string{
				"cluster-autoscaler/node-template/autoscaling-options/scaleDownUtilizationThreshold": "0.5",
			},
			wantOptions: map[string]string{"scaleDownUtilizationThreshold": "0.5"},
		},
		{
			name: "autoscaling option from defined tag",
			definedTags: map[string]map[string]string{
				ns: {"cluster-autoscaler/node-template/autoscaling-options/scaleDownUtilizationThreshold": "0.5"},
			},
			wantOptions: map[string]string{"scaleDownUtilizationThreshold": "0.5"},
		},
		{
			name: "autoscaling option with period substitution",
			freeformTags: map[string]string{
				"cluster-autoscaler/node-template/autoscaling-options/custom~2option": "value",
			},
			wantOptions: map[string]string{"custom.option": "value"},
		},
		// --- Empty / no-op ---
		{
			name: "empty tags produce empty attributes",
		},
		{
			name: "unrelated tags are ignored",
			freeformTags: map[string]string{
				"other-tag":       "value1",
				"unrelated-label": "value2",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NewNodeTemplater(ns).ExtractNodeTemplateAttributes(tt.freeformTags, tt.definedTags)

			// Check labels
			if len(result.Labels) != len(tt.wantLabels) {
				t.Errorf("labels: got %d entries, want %d: %v vs %v", len(result.Labels), len(tt.wantLabels), result.Labels, tt.wantLabels)
			}
			for k, v := range tt.wantLabels {
				if result.Labels[k] != v {
					t.Errorf("label %s: got %q, want %q", k, result.Labels[k], v)
				}
			}

			// Check taints (order-independent)
			if len(result.Taints) != len(tt.wantTaints) {
				t.Errorf("taints: got %d entries, want %d: %v vs %v", len(result.Taints), len(tt.wantTaints), result.Taints, tt.wantTaints)
			}
			for _, want := range tt.wantTaints {
				found := false
				for _, got := range result.Taints {
					if got.Key == want.Key && got.Value == want.Value && got.Effect == want.Effect {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("taint %+v not found in result %v", want, result.Taints)
				}
			}

			// Check resources
			if len(result.Resources) != len(tt.wantResources) {
				t.Errorf("resources: got %d entries, want %d", len(result.Resources), len(tt.wantResources))
			}
			for k, v := range tt.wantResources {
				wantQty := resource.MustParse(v)
				if result.Resources[k] == nil {
					t.Errorf("resource %s not found in result", k)
					continue
				}
				if !result.Resources[k].Equal(wantQty) {
					t.Errorf("resource %s: got %s, want %s", k, result.Resources[k].String(), v)
				}
			}

			// Check autoscaling options
			if len(result.AutoscalingOptions) != len(tt.wantOptions) {
				t.Errorf("autoscaling options: got %d entries, want %d: %v vs %v", len(result.AutoscalingOptions), len(tt.wantOptions), result.AutoscalingOptions, tt.wantOptions)
			}
			for k, v := range tt.wantOptions {
				if result.AutoscalingOptions[k] != v {
					t.Errorf("autoscaling option %s: got %q, want %q", k, result.AutoscalingOptions[k], v)
				}
			}
		})
	}
}
