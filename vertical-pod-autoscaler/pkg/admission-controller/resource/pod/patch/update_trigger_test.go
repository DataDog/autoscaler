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

package patch

import (
	"testing"
	"time"

	core "k8s.io/api/core/v1"

	resource_admission "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/admission-controller/resource"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/annotations"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/test"

	"github.com/stretchr/testify/assert"
)

func addVpaUpdateTriggerPatch(value string) *resource_admission.PatchRecord {
	patch := GetAddAnnotationPatch(annotations.VpaTriggerLabel, value)
	return &patch
}

func TestCalculatePatches_UpdateTrigger(t *testing.T) {
	now = func() time.Time { return time.Date(1, 1, 1, 1, 1, 1, 1, time.UTC) }
	defer func() {
		now = time.Now
	}()

	tests := []struct {
		name          string
		pod           *core.Pod
		expectedPatch *resource_admission.PatchRecord
	}{
		{
			name:          "doesn't update pod without trigger annotation",
			pod:           test.Pod().Get(),
			expectedPatch: nil,
		},
		{
			name:          "update vpa update trigger annotation",
			pod:           test.Pod().WithAnnotations(map[string]string{annotations.VpaTriggerLabel: annotations.VpaTriggerEnabled}).Get(),
			expectedPatch: addVpaUpdateTriggerPatch(annotations.GetVpaTriggeredValue(now())),
		},
		{
			name:          "doesn't vpa update trigger annotation if already triggered",
			pod:           test.Pod().WithAnnotations(map[string]string{annotations.VpaTriggerLabel: annotations.GetVpaTriggeredValue(now())}).Get(),
			expectedPatch: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := NewUpdateTriggerCalculator()
			patches, err := c.CalculatePatches(tc.pod, nil)
			assert.NoError(t, err)
			if tc.expectedPatch != nil {
				if assert.Len(t, patches, 1, "Unexpected number of patches.") {
					AssertEqPatch(t, *tc.expectedPatch, patches[0])
				}
			} else {
				assert.Empty(t, patches)
			}
		})
	}
}
