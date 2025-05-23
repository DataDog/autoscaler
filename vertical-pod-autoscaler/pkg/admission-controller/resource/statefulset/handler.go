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

package statefulset

import (
	"encoding/json"
	"fmt"

	admissionv1 "k8s.io/api/admission/v1"
	v1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	resource_admission "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/admission-controller/resource"

	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/admission-controller/resource/pod"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/admission-controller/resource/pod/patch"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/admission-controller/resource/vpa"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/admission-controller/resource/workloads"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/metrics/admission"
	"k8s.io/klog/v2"
)

// resourceHandler builds patches for StatefulSets.
type resourceHandler struct {
	workloads.WorkloadResourceHandler
}

// NewResourceHandler creates new instance of resourceHandler.
func NewResourceHandler(preProcessor pod.PreProcessor, vpaMatcher vpa.Matcher, patchCalculators []patch.Calculator) resource_admission.Handler {
	return &resourceHandler{
		*workloads.NewResourceHandler(preProcessor, vpaMatcher, patchCalculators),
	}
}

// AdmissionResource returns resource type this handler accepts.
func (h *resourceHandler) AdmissionResource() admission.AdmissionResource {
	return admission.StatefulSet
}

// GroupResource returns Group and Resource type this handler accepts.
func (h *resourceHandler) GroupResource() metav1.GroupResource {
	return metav1.GroupResource{Group: "apps", Resource: "statefulsets"}
}

// DisallowIncorrectObjects decides whether incorrect objects (eg. unparsable, not passing validations) should be disallowed by Admission Server.
func (h *resourceHandler) DisallowIncorrectObjects() bool {
	// Incorrect StatefulSets are validated by API Server.
	return false
}

// GetPatches builds patches for StatefulSets in given admission request.
func (h *resourceHandler) GetPatches(ar *admissionv1.AdmissionRequest) ([]resource_admission.PatchRecord, error) {
	if ar.Resource.Version != "v1" {
		return nil, fmt.Errorf("only v1 statefulsets are supported")
	}
	raw, namespace := ar.Object.Raw, ar.Namespace
	sts := v1.StatefulSet{}
	if err := json.Unmarshal(raw, &sts); err != nil {
		return nil, err
	}

	klog.V(4).Infof("Admitting statefulset %v", sts.ObjectMeta)

	return h.GetPatchesForWorkload(namespace, &sts.ObjectMeta, &sts.Spec.Template)
}
