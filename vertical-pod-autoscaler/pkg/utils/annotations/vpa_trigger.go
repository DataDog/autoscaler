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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// VpaTriggerLabel is a label used by the vpa trigger annotation.
	VpaTriggerLabel = "vpaTrigger"
	// VpaTriggerEnabled is the value of VpaTriggerLabel used to enable the tirgger.
	VpaTriggerEnabled = "true"
	// VpaTriggerTriggered is the value of VpaTriggerLabel set once we've triggered an update because of the annotation.
	VpaTriggerTriggered = "triggered at"
)

// HasVpaTrigger creates an annotation value for a given pod.
func HasVpaTrigger(objectMeta *metav1.ObjectMeta) bool {
	if val, ok := objectMeta.Annotations[VpaTriggerLabel]; ok && val == VpaTriggerEnabled {
		return true
	}
	return false
}

// GetVpaTriggeredValue returns the value for the VpaTriggerLabel annotation once it has been triggered at a given time.
func GetVpaTriggeredValue(t time.Time) string {
	return VpaTriggerTriggered + " " + t.Format(time.RFC3339)
}
