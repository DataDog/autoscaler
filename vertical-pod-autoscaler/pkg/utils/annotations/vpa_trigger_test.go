/*
Copyright 2020 The Kubernetes Authors.

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

package annotations

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"

	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/test"
)

func TestHasVpaTrigger(t *testing.T) {
	tests := []struct {
		name string
		pod  *v1.Pod
		want bool
	}{
		{
			name: "without annotation",
			pod:  test.Pod().Get(),
			want: false,
		},
		{
			name: "with annotation false",
			pod:  test.Pod().WithAnnotations(map[string]string{VpaTriggerLabel: "false"}).Get(),
			want: false,
		},
		{
			name: "with annotation true",
			pod:  test.Pod().WithAnnotations(map[string]string{VpaTriggerLabel: "true"}).Get(),
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("test case: %s", tc.name), func(t *testing.T) {
			got := HasVpaTrigger(&tc.pod.ObjectMeta)
			assert.Equal(t, got, tc.want)
		})
	}
}