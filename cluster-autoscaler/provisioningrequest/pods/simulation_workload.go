/*
Copyright The Kubernetes Authors.

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

package pods

import (
	"crypto/sha256"
	"fmt"
	"maps"
	"strings"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/autoscaler/cluster-autoscaler/provisioningrequest/provreqwrapper"
	resourcelisters "k8s.io/client-go/listers/resource/v1"
	"k8s.io/utils/ptr"
)

const (
	maxObjectNameLength       = 253
	materializedClaimHashSize = 16
)

// SimulationWorkload contains virtual Pods and all ResourceClaims which must
// be present in the same ClusterSnapshot transaction when scheduling them.
type SimulationWorkload struct {
	Pods   []*corev1.Pod
	Claims []*resourcev1.ResourceClaim
}

// SimulationWorkloadBuilder expands ProvisioningRequests and materializes
// ResourceClaimTemplate-backed claims without writing Kubernetes API objects.
type SimulationWorkloadBuilder struct {
	resourceClaimTemplateLister resourcelisters.ResourceClaimTemplateLister
}

// NewSimulationWorkloadBuilder creates a builder backed by the shared
// ResourceClaimTemplate informer cache.
func NewSimulationWorkloadBuilder(resourceClaimTemplateLister resourcelisters.ResourceClaimTemplateLister) *SimulationWorkloadBuilder {
	return &SimulationWorkloadBuilder{resourceClaimTemplateLister: resourceClaimTemplateLister}
}

// ForProvisioningRequest expands a ProvisioningRequest and returns its complete
// in-memory scheduling workload.
func (b *SimulationWorkloadBuilder) ForProvisioningRequest(pr *provreqwrapper.ProvisioningRequest) (*SimulationWorkload, error) {
	pods, err := PodsForProvisioningRequest(pr)
	if err != nil {
		return nil, err
	}
	workload, err := b.ForPods(pods)
	if err != nil && pr != nil {
		return nil, fmt.Errorf("ProvisioningRequest %s/%s: %w", pr.Namespace, pr.Name, err)
	}
	return workload, err
}

// ForPods deep-copies already-expanded virtual Pods and materializes their
// ResourceClaimTemplate-backed claims. It is deterministic and idempotent.
func (b *SimulationWorkloadBuilder) ForPods(pods []*corev1.Pod) (*SimulationWorkload, error) {
	workload := &SimulationWorkload{
		Pods:   make([]*corev1.Pod, 0, len(pods)),
		Claims: make([]*resourcev1.ResourceClaim, 0),
	}
	materializedClaimNames := make(map[string]string)

	for i, sourcePod := range pods {
		if sourcePod == nil {
			return nil, fmt.Errorf("virtual pod at index %d is nil", i)
		}
		pod := sourcePod.DeepCopy()
		if pod.Namespace == "" || pod.Name == "" || pod.UID == "" {
			return nil, fmt.Errorf("virtual pod at index %d must have namespace, name, and UID before ResourceClaimTemplate materialization", i)
		}
		seenLogicalClaims := make(map[string]struct{}, len(pod.Spec.ResourceClaims))

		for claimIndex := range pod.Spec.ResourceClaims {
			podClaim := &pod.Spec.ResourceClaims[claimIndex]
			if podClaim.Name == "" {
				return nil, fmt.Errorf("virtual pod %s/%s has a resource claim with an empty logical name", pod.Namespace, pod.Name)
			}
			if _, found := seenLogicalClaims[podClaim.Name]; found {
				return nil, fmt.Errorf("virtual pod %s/%s has duplicate logical resource claim %q", pod.Namespace, pod.Name, podClaim.Name)
			}
			seenLogicalClaims[podClaim.Name] = struct{}{}

			directName := podClaim.ResourceClaimName
			templateName := podClaim.ResourceClaimTemplateName
			switch {
			case directName != nil && templateName != nil:
				return nil, fmt.Errorf("virtual pod %s/%s resource claim %q sets both resourceClaimName and resourceClaimTemplateName", pod.Namespace, pod.Name, podClaim.Name)
			case directName == nil && templateName == nil:
				return nil, fmt.Errorf("virtual pod %s/%s resource claim %q has no resource claim source", pod.Namespace, pod.Name, podClaim.Name)
			case directName != nil:
				if *directName == "" {
					return nil, fmt.Errorf("virtual pod %s/%s resource claim %q has an empty resourceClaimName", pod.Namespace, pod.Name, podClaim.Name)
				}
				continue
			}

			if *templateName == "" {
				return nil, fmt.Errorf("virtual pod %s/%s resource claim %q has an empty resourceClaimTemplateName", pod.Namespace, pod.Name, podClaim.Name)
			}
			if b == nil || b.resourceClaimTemplateLister == nil {
				return nil, fmt.Errorf("virtual pod %s/%s resource claim %q references ResourceClaimTemplate %s/%s, but no ResourceClaimTemplate lister is configured", pod.Namespace, pod.Name, podClaim.Name, pod.Namespace, *templateName)
			}

			template, err := b.resourceClaimTemplateLister.ResourceClaimTemplates(pod.Namespace).Get(*templateName)
			if err != nil {
				return nil, fmt.Errorf("virtual pod %s/%s resource claim %q could not get ResourceClaimTemplate %s/%s: %w", pod.Namespace, pod.Name, podClaim.Name, pod.Namespace, *templateName, err)
			}

			claimName := simulationResourceClaimName(pod, podClaim.Name)
			key := pod.Namespace + "/" + claimName
			if previous, found := materializedClaimNames[key]; found {
				return nil, fmt.Errorf("virtual pod %s/%s resource claim %q generates duplicate ResourceClaim %s already generated for %s", pod.Namespace, pod.Name, podClaim.Name, key, previous)
			}
			materializedClaimNames[key] = pod.Namespace + "/" + pod.Name + ":" + podClaim.Name

			statusFound := false
			for statusIndex := range pod.Status.ResourceClaimStatuses {
				claimStatus := &pod.Status.ResourceClaimStatuses[statusIndex]
				if claimStatus.Name != podClaim.Name {
					continue
				}
				if statusFound || claimStatus.ResourceClaimName == nil || *claimStatus.ResourceClaimName != claimName {
					return nil, fmt.Errorf("virtual pod %s/%s resource claim %q has conflicting status mapping for ResourceClaim %s", pod.Namespace, pod.Name, podClaim.Name, claimName)
				}
				statusFound = true
			}
			if !statusFound {
				pod.Status.ResourceClaimStatuses = append(pod.Status.ResourceClaimStatuses, corev1.PodResourceClaimStatus{
					Name:              podClaim.Name,
					ResourceClaimName: ptr.To(claimName),
				})
			}

			annotations := maps.Clone(template.Spec.Annotations)
			if annotations == nil {
				annotations = make(map[string]string, 1)
			}
			annotations[resourcev1.PodResourceClaimAnnotation] = podClaim.Name
			claim := &resourcev1.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:        claimName,
					Namespace:   pod.Namespace,
					UID:         types.UID(pod.Namespace + "/" + claimName),
					Labels:      maps.Clone(template.Spec.Labels),
					Annotations: annotations,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: corev1.SchemeGroupVersion.String(),
							Kind:       "Pod",
							Name:       pod.Name,
							UID:        pod.UID,
							Controller: ptr.To(true),
						},
					},
				},
				Spec: *template.Spec.Spec.DeepCopy(),
			}
			workload.Claims = append(workload.Claims, claim)
		}
		workload.Pods = append(workload.Pods, pod)
	}

	return workload, nil
}

func simulationResourceClaimName(pod *corev1.Pod, logicalClaimName string) string {
	identity := fmt.Sprintf("%s\x00%s\x00%s\x00%s", pod.Namespace, pod.Name, pod.UID, logicalClaimName)
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(identity)))[:materializedClaimHashSize]
	suffix := "-" + hash
	prefix := pod.Name + "-" + logicalClaimName
	if len(prefix)+len(suffix) > maxObjectNameLength {
		prefix = prefix[:maxObjectNameLength-len(suffix)]
	}
	prefix = strings.TrimRight(prefix, ".-")
	if prefix == "" {
		prefix = "simulated-claim"
	}
	return prefix + suffix
}
