/*
Copyright 2021 The Kubernetes Authors.

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

package workloads

import (
	"strings"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	resource_admission "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/admission-controller/resource"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/admission-controller/resource/pod"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/admission-controller/resource/pod/patch"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/admission-controller/resource/vpa"
	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/annotations"
	vpa_api_util "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/vpa"
)

// WorkloadResourceHandler builds patches for Workloads.
type WorkloadResourceHandler struct {
	preProcessor     pod.PreProcessor
	vpaMatcher       vpa.Matcher
	patchCalculators []patch.Calculator
}

// NewResourceHandler creates new instance of resourceHandler.
func NewResourceHandler(preProcessor pod.PreProcessor, vpaMatcher vpa.Matcher, patchCalculators []patch.Calculator) *WorkloadResourceHandler {
	return &WorkloadResourceHandler{
		preProcessor:     preProcessor,
		vpaMatcher:       vpaMatcher,
		patchCalculators: patchCalculators,
	}
}

// GetPatchesForWorkload builds patches for Pod in given admission request.
func (h *WorkloadResourceHandler) GetPatchesForWorkload(namespace string, objectMeta *metav1.ObjectMeta, podTemplateSpec *v1.PodTemplateSpec) ([]resource_admission.PatchRecord, error) {
	if len(objectMeta.Name) == 0 {
		objectMeta.Name = objectMeta.GenerateName + "%"
		objectMeta.Namespace = namespace
	}

	// We create a `Pod` to be able to re-use the pod code.
	pod := PodFromPodTemplateSpec(objectMeta, podTemplateSpec)
	klog.V(4).Infof("Generating pod %s/%s", pod.Namespace, pod.Name)
	controllingVpa := h.vpaMatcher.GetMatchingVPA(&pod)
	if controllingVpa == nil {
		klog.V(4).Infof("No matching VPA found for %s/%s", objectMeta.Namespace, objectMeta.Name)
		return []resource_admission.PatchRecord{}, nil
	}
	updateMode := vpa_api_util.GetUpdateMode(controllingVpa)
	if updateMode != vpa_types.UpdateModeTrigger {
		klog.V(4).Infof("Only UpdateMode=%s is supported for workload, found %s", vpa_types.UpdateModeTrigger, updateMode)
		return []resource_admission.PatchRecord{}, nil
	}
	pod, err := h.preProcessor.Process(pod)
	if err != nil {
		return nil, err
	}

	patches := []resource_admission.PatchRecord{}
	if objectMeta.Annotations == nil {
		patches = append(patches, patch.GetAddEmptyAnnotationsPatch())
	}
	for _, c := range h.patchCalculators {
		partialPatches, err := c.CalculatePatches(&pod, controllingVpa)
		if err != nil {
			return []resource_admission.PatchRecord{}, err
		}
		patches = append(patches, partialPatches...)
	}

	return FixupPatchesSpecPathForWorkload(patches), nil
}

// FixupPatchesSpecPathForWorkload takes patches for a pod and adapt them to be applied to a workload.
func FixupPatchesSpecPathForWorkload(patches []resource_admission.PatchRecord) []resource_admission.PatchRecord {
	for i, patch := range patches {
		// If it's a patch to spec, add the path to the PodSpec
		if strings.HasPrefix(patch.Path, "/spec/") {
			patches[i].Path = "/spec/template" + patch.Path
		}
	}
	return patches
}

// PodFromPodTemplateSpec creates a fake Pod object from a PodTemplateSpec. This is used by the workload
// admission controllers tu re-use as much code as we can without modifying it, it makes rebases easier.
// If we ever want to upstream this code we should make the pod code more generic.
func PodFromPodTemplateSpec(objectMeta *metav1.ObjectMeta, podTemplateSpec *v1.PodTemplateSpec) v1.Pod {
	pod := v1.Pod{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Pod",
			APIVersion: "v1",
		},
		// This is important to get the same labels as the pod (which need to match the selector)
		ObjectMeta: podTemplateSpec.ObjectMeta,
		Spec:       podTemplateSpec.Spec,
		Status:     v1.PodStatus{},
	}
	// We also need to propagate the trigger annotation that was likely set on the parent.
	if annotations.HasVpaTrigger(objectMeta) {
		if pod.Annotations == nil {
			pod.Annotations = make(map[string]string)
		}
		pod.Annotations[annotations.VpaTriggerLabel] = annotations.VpaTriggerEnabled
	}
	if pod.Name == "" {
		pod.Name = objectMeta.Name + "%"
	}
	if pod.Namespace == "" {
		pod.Namespace = objectMeta.Namespace
	}
	return pod
}
