package input

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
)

func TestGetVpaExternalMetrics(t *testing.T) {
	type args struct {
		annotations map[string]string
	}
	tests := []struct {
		name string
		args args
		want ContainersToResourcesAndMetrics
	}{
		{
			name: "empty",
			args: args{
				annotations: map[string]string{},
			},
			want: ContainersToResourcesAndMetrics{},
		},
		{
			name: "some annotations",
			args: args{
				annotations: map[string]string{
					AnnotationKey("container1", model.ResourceCPU):    "system-cpu",
					AnnotationKey("container1", model.ResourceMemory): "system-memory",
				},
			},
			want: ContainersToResourcesAndMetrics{
				"container1": {
					model.ResourceCPU:    "system-cpu",
					model.ResourceMemory: "system-memory",
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, GetVpaExternalMetrics(tt.args.annotations), "GetVpaExternalMetrics(%v)", tt.args.annotations)
		})
	}
}
