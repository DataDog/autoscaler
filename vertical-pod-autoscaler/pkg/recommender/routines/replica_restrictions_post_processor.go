/*
Copyright 2022 The Kubernetes Authors.

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

package routines

import (
	"encoding/json"
	"fmt"

	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"

	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	controllerfetcher "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/input/controller_fetcher"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
)

const (
	vpaPostProcessorReplicaRestrictedRangeSuffix = "replicaRestrictedRange"
)

// ReplicaRestrictionsPostProcessor ensures that defined ratio constraints between resources is applied.
// The definition is done via annotation on the VPA object with format: vpa-post-processor.kubernetes.io/replicaRestictedRange="[downscaleIfLessReplicasThan, upscaleIfMoreReplicasThan]"
// - downscaleIfLessReplicasThan is the maximum number of replicas to allow a vertical downscale
// - upscaleIfMoreReplicasThan is the minimum number of replicas to allow a vertical upscale
//
// In other words:
// - If we have less than <downscaleIfLessReplicasThan> replicas, we will allow the pods to be smaller, the intent it to keep the number
//   of pods above <downscaleIfLessReplicasThan>. This allows you to improve resource utilisation to avoid having few very big and underutilised pods.
// - If we have more than <upscaleIfMoreReplicasThan> replicas, we will allow the pods to be bigger, the intent it to keep the number
//   of pods below <upscaleIfMoreReplicasThan>. This allows you to reduce the pressure on the k8s control plane.
//
// Any resource increase is considered an upscale, any resource decrease is considered a downscale. This means that they
// are not mutually exclusive. When controlling multiple resources it makes sense to use another processor (`maintainedRatio` policy) to ensure
// that the ratio between the resources is maintained.
type ReplicaRestrictionsPostProcessor struct {
	ControllerFetcher controllerfetcher.ControllerFetcher
}

// Process applies the Resource Ratio post-processing to the recommendation.
func (r *ReplicaRestrictionsPostProcessor) Process(vpa *model.Vpa, recommendation *vpa_types.RecommendedPodResources, _ *vpa_types.PodResourcePolicy) *vpa_types.RecommendedPodResources {
	if recommendation == nil {
		// If there is no recommendation let's skip that post-processor.
		return recommendation
	}

	replicaRange, err := readRangeFromVPAAnnotations(vpa)
	if err != nil {
		klog.Error("Can't read replica range from annotation", err)
	}
	if replicaRange == nil || len(replicaRange) != 2 || recommendation == nil {
		return recommendation
	}
	downscaleIfLessReplicasThan, upscaleIfMoreReplicasThan := replicaRange[0], replicaRange[1]

	if downscaleIfLessReplicasThan >= upscaleIfMoreReplicasThan {
		klog.Errorf("Skipping ReplicaRestrictionsPostProcessor for vpa %s/%s due to bad range: %d >= %d", vpa.ID.Namespace, vpa.ID.VpaName, downscaleIfLessReplicasThan, upscaleIfMoreReplicasThan)
		return recommendation
	}

	if *vpa.UpdateMode != vpa_types.UpdateModeOff  && *vpa.UpdateMode != vpa_types.UpdateModeTrigger {
		klog.Errorf("Skipping ReplicaRestrictionsPostProcessor for vpa %s/%s due to update mode: %s", vpa.ID.Namespace, vpa.ID.VpaName, vpa.UpdateMode)
		return recommendation
	}

	podTemplate, err := r.ControllerFetcher.GetPodTemplateFromTopMostWellKnown(&controllerfetcher.ControllerKeyWithAPIVersion{
		ControllerKey: controllerfetcher.ControllerKey{
			Namespace: vpa.ID.Namespace,
			Kind:      vpa.TargetRef.Kind,
			Name:      vpa.TargetRef.Name,
		},
		ApiVersion: vpa.TargetRef.APIVersion,
	})
	if err != nil {
		klog.Errorf("Failed to apply ReplicaRestrictionsPostProcessor (controller fetch) to vpa %s/%s due to error: %#v", vpa.ID.Namespace, vpa.ID.VpaName, err)
		return recommendation
	}

	pod := newPodFromTemplate(podTemplate)

	updatedRecommendation, err := r.apply(recommendation, vpa.PodCount, downscaleIfLessReplicasThan, upscaleIfMoreReplicasThan, pod)
	if err != nil {
		klog.Errorf("Failed to apply ReplicaRestrictionsPostProcessor to vpa %s/%s due to error: %#v", vpa.ID.Namespace, vpa.ID.VpaName, err)
	}
	return updatedRecommendation
}

func readRangeFromVPAAnnotations(vpa *model.Vpa) ([]int, error) {

	for key, value := range vpa.Annotations {
		if key != vpaPostProcessorPrefix +vpaPostProcessorReplicaRestrictedRangeSuffix {
			continue
		}

		var replicaRange []int
		if err := json.Unmarshal([]byte(value), &replicaRange); err != nil {
			return nil, fmt.Errorf("skipping restrictions definition '%s' in vpa %s/%s due to bad format, error:%#v", value, vpa.ID.Namespace, vpa.ID.VpaName, err)

		}
		if len(replicaRange) != 2 {
			return nil, fmt.Errorf("skipping restrictions definition '%s' in vpa %s/%s due to bad format", value, vpa.ID.Namespace, vpa.ID.VpaName)
		}
		return replicaRange, nil
	}
	return nil, nil
}

// ReplicaRestrictionsPostProcessor must implement RecommendationProcessor
var _ RecommendationPostProcessor = &ReplicaRestrictionsPostProcessor{}

// Apply returns a recommendation for the given pod, adjusted to obey maintainedRatio policy
func (r *ReplicaRestrictionsPostProcessor) apply(
	podRecommendation *vpa_types.RecommendedPodResources,
	replicas, downscaleIfLessReplicasThan, upscaleIfMoreReplicasThan int,
	pod *apiv1.Pod) (*vpa_types.RecommendedPodResources, error) {
	updatedRecommendations := []vpa_types.RecommendedContainerResources{}

	for _, containerRecommendation := range podRecommendation.ContainerRecommendations {
		container := getContainer(containerRecommendation.ContainerName, pod)
		if container == nil {
			klog.V(2).Infof("no matching Container found for recommendation %s", containerRecommendation.ContainerName)
			continue
		}

		updatedContainerResources, err := getRecommendationForContainerWithRestrictedScaling(*container, &containerRecommendation, replicas, downscaleIfLessReplicasThan, upscaleIfMoreReplicasThan)
		if err != nil {
			return nil, fmt.Errorf("cannot update recommendation for container name %v", container.Name)
		}
		updatedRecommendations = append(updatedRecommendations, *updatedContainerResources)
	}

	return &vpa_types.RecommendedPodResources{ContainerRecommendations: updatedRecommendations}, nil
}

// getRecommendationForContainerWithRestrictedScaling returns a recommendation for the given container, adjusted to obey maintainedRatios policy
func getRecommendationForContainerWithRestrictedScaling(
	container apiv1.Container,
	containerRecommendation *vpa_types.RecommendedContainerResources,
	replicas int,
	downscaleIfLessReplicasThan, upscaleIfMoreReplicasThan int) (*vpa_types.RecommendedContainerResources, error) {

	amendedRecommendations := containerRecommendation.DeepCopy()

	isRestricted := isRestrictedScaling(containerRecommendation, container, downscaleIfLessReplicasThan, upscaleIfMoreReplicasThan, replicas)
	process := func(recommendation apiv1.ResourceList) {
		applyRestrictedScalingVPAPolicy(recommendation, isRestricted, container.Resources.Requests)
	}

	process(amendedRecommendations.Target)
	// Do we want to change LowerBound and UpperBound?

	return amendedRecommendations, nil
}

// isRestrictedScaling: detects if the recommendation is a restricted scaling
func isRestrictedScaling(containerRecommendation *vpa_types.RecommendedContainerResources, container apiv1.Container, downscaleIfLessReplicasThan, upscaleIfMoreReplicasThan int, replicas int) bool {
	isAnUpscale := isUpscale(containerRecommendation.Target, container.Resources.Requests)
	isADownscale := isDownscale(containerRecommendation.Target, container.Resources.Requests)
	isRestricted := false
	if isAnUpscale && replicas <= upscaleIfMoreReplicasThan {
		isRestricted = true
	}
	if isADownscale && replicas >= downscaleIfLessReplicasThan {
		isRestricted = true
	}
	if isRestricted {
		klog.V(2).Infof("[%s] Scaling is restricted %d - replica restrictions [%d, %d] - upscale: %t - downscale: %t", container.Name, replicas, downscaleIfLessReplicasThan, upscaleIfMoreReplicasThan, isAnUpscale, isADownscale)
	} else {
		klog.V(4).Infof("[%s] Scaling is not restricted %d - replica restrictions [%d, %d] - upscale: %t - downscale: %t", container.Name, replicas, downscaleIfLessReplicasThan, upscaleIfMoreReplicasThan, isAnUpscale, isADownscale)
	}
	return isRestricted
}

// isUpscale: detects if the recommendation is an upscale. Any increase of resources is considered as upscale.
func isUpscale(recommendation apiv1.ResourceList, containerOriginalResources apiv1.ResourceList) bool {
	for recommendationType := range recommendation {
		q := containerOriginalResources.Name(recommendationType, resource.DecimalSI).MilliValue()
		r := recommendation.Name(recommendationType, resource.DecimalSI).MilliValue()
		if r > q {
			return true
		}
	}
	return false
}

// isDownscale: detects if the recommendation is a downscale. Any decrease of resources is considered as downscale.
func isDownscale(recommendation apiv1.ResourceList, containerOriginalResources apiv1.ResourceList) bool {
	for recommendationType := range recommendation {
		q := containerOriginalResources.Name(recommendationType, resource.DecimalSI).MilliValue()
		r := recommendation.Name(recommendationType, resource.DecimalSI).MilliValue()
		if r < q {
			return true
		}
	}
	return false
}

// applyRestrictedScalingVPAPolicy makes sure we do not make a recommendation if we are restricted.
func applyRestrictedScalingVPAPolicy(recommendation apiv1.ResourceList, isRestricted bool, containerOriginalResources apiv1.ResourceList) {

	for recommendationType := range recommendation {
		if isRestricted {
			recommendation[recommendationType] = containerOriginalResources[recommendationType]
		}
	}
	return
}
