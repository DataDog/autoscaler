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

package input

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
)

func TestGetVpaExternalMetrics(t *testing.T) {
	type args struct {
	}
	tests := []struct {
		name        string
		annotations map[string]string
		want        ContainersToResourcesAndMetrics
	}{
		{
			name:        "empty",
			annotations: map[string]string{},
			want:        ContainersToResourcesAndMetrics{},
		},
		{
			name: "some annotations",
			annotations: map[string]string{
				AnnotationKey("container1", model.ResourceCPU):    "system-cpu",
				AnnotationKey("container1", model.ResourceMemory): "system-memory",
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
			assert.Equalf(t, tt.want, GetVpaExternalMetrics(tt.annotations), "GetVpaExternalMetrics(%v)", tt.annotations)
		})
	}
}
