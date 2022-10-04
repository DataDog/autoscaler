/*
Copyright 2018 The Kubernetes Authors.

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

package recommendation

import (
	"flag"
	"fmt"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	vpa_annotations "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/annotations"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/limitrange"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/qos"
	vpa_api_util "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/vpa"
	"k8s.io/klog/v2"
)

var (
	percentageFlag      = flag.Int("percentage", 0, "Percent (int, 0-100) of recommendation to use over original request.")
	percentErrorPrinted = false
)

// Provider gets current recommendation, annotations and vpaName for the given pod.
type Provider interface {
	GetContainersResourcesForPod(pod *core.Pod, vpa *vpa_types.VerticalPodAutoscaler) ([]vpa_api_util.ContainerResources, vpa_api_util.ContainerToAnnotationsMap, error)
}

type recommendationProvider struct {
	limitsRangeCalculator   limitrange.LimitRangeCalculator
	recommendationProcessor vpa_api_util.RecommendationProcessor
	enableQoS               bool
}

// NewProvider constructs the recommendation provider that can be used to determine recommendations for pods.
func NewProvider(calculator limitrange.LimitRangeCalculator,
	recommendationProcessor vpa_api_util.RecommendationProcessor, enableQoS bool) Provider {
	return &recommendationProvider{
		limitsRangeCalculator:   calculator,
		recommendationProcessor: recommendationProcessor,
		enableQoS:               enableQoS,
	}
}

// percentageToTarget returns an interpolated value between Requests and target, where a proportion of 0 is Requests, and 1.0 is target.
func percentageToTarget(requests core.ResourceList, target core.ResourceList, proportion float64) core.ResourceList {
	reqMem, okReq := requests.Memory().AsInt64()
	targetMem, okTarget := target.Memory().AsInt64()
	if !okReq || !okTarget {
		return requests
	}
	cpuDiff := target.Cpu().MilliValue() - requests.Cpu().MilliValue()
	memDiff := targetMem - reqMem

	// For QoS'd workloads, where the target core count is an integer, round the proportion to an integer too.
	cpuQuantity := requests.Cpu().MilliValue() + int64(float64(cpuDiff)*proportion)
	result := core.ResourceList{
		core.ResourceCPU:    *resource.NewMilliQuantity(cpuQuantity, target.Cpu().Format),
		core.ResourceMemory: *resource.NewQuantity(reqMem+int64(float64(memDiff)*proportion), target.Memory().Format),
	}
	return result
}

// GetContainersResources returns the recommended resources for each container in the given pod in the same order they are specified in the pod.Spec.
// If addAll is set to true, containers w/o a recommendation are also added to the list, otherwise they're skipped (default behaviour).  If enableQos
// is set to true, turn QoS Guaranteed (Limits=cores) when the VPA annotations are right and the core count is integral.
func GetContainersResources(pod *core.Pod, vpaResourcePolicy *vpa_types.PodResourcePolicy, podRecommendation vpa_types.RecommendedPodResources, limitRange *core.LimitRangeItem,
	addAll bool, annotations vpa_api_util.ContainerToAnnotationsMap, vpaAnnotations map[string]string, enableQoS bool) []vpa_api_util.ContainerResources {
	var proportion = float64(*percentageFlag) / 100.0
	if (proportion < 0 || proportion > 1.0) && !percentErrorPrinted {
		klog.Errorf("Invalid value for --percentage.  Treating as 100%.")
		proportion = 1.0
		percentErrorPrinted = true
	}
	resources := make([]vpa_api_util.ContainerResources, len(pod.Spec.Containers))
	options, _ := vpa_annotations.ParseDatadogExtensions(vpaAnnotations)
	inQoS := enableQoS && qos.NeedsQoS(options)
	for i, container := range pod.Spec.Containers {
		recommendation := vpa_api_util.GetRecommendationForContainer(container.Name, &podRecommendation)
		if recommendation == nil {
			if !addAll {
				klog.V(2).Infof("%v/%v: no matching recommendation found for container %s, skipping", pod.Namespace, pod.Name, container.Name)
				continue
			}
			klog.V(2).Infof("%v/%v: no matching recommendation found for container %s, using its request", pod.Namespace, pod.Name, container.Name)
			resources[i].Requests = container.Resources.Requests
		} else {
			scaledResources := percentageToTarget(container.Resources.Requests, recommendation.Target, proportion)
			resources[i].Requests = qos.RoundResourceListToPolicy(options, scaledResources)
			klog.V(2).Infof("%v/%v:%v: With --percentage %v, requests %+v, target %+v, scaled to %+v, setting new requests to %+v",
				pod.Namespace, pod.Name, container.Name, *percentageFlag, container.Resources.Requests, recommendation.Target, scaledResources, resources[i].Requests)
		}
		defaultLimit := core.ResourceList{}
		if limitRange != nil {
			defaultLimit = limitRange.Default
		}
		containerControlledValues := vpa_api_util.GetContainerControlledValues(container.Name, vpaResourcePolicy)
		if containerControlledValues == vpa_types.ContainerControlledValuesRequestsAndLimits {
			var proportionalLimits core.ResourceList
			var limitAnnotations []string
			if inQoS {
				proportionalLimits, limitAnnotations = vpa_api_util.GetQoSLimit(container.Resources.Limits, container.Resources.Requests, resources[i].Requests, defaultLimit)
			} else {
				proportionalLimits, limitAnnotations = vpa_api_util.GetProportionalLimit(container.Resources.Limits, container.Resources.Requests, resources[i].Requests, defaultLimit)
			}
			if proportionalLimits != nil {
				resources[i].Limits = proportionalLimits
				if len(limitAnnotations) > 0 {
					annotations[container.Name] = append(annotations[container.Name], limitAnnotations...)
				}
			}
		}
	}
	return resources
}

// GetContainersResourcesForPod returns recommended request for a given pod and associated annotations.
// The returned slice corresponds 1-1 to containers in the Pod.
func (p *recommendationProvider) GetContainersResourcesForPod(pod *core.Pod, vpa *vpa_types.VerticalPodAutoscaler) ([]vpa_api_util.ContainerResources, vpa_api_util.ContainerToAnnotationsMap, error) {
	if vpa == nil || pod == nil {
		klog.V(2).Infof("can't calculate recommendations, one of vpa(%+v), pod(%+v) is nil", vpa, pod)
		return nil, nil, nil
	}
	klog.V(2).Infof("updating requirements for pod %s.", pod.Name)

	var annotations vpa_api_util.ContainerToAnnotationsMap
	recommendedPodResources := &vpa_types.RecommendedPodResources{}

	if vpa.Status.Recommendation != nil {
		var err error
		recommendedPodResources, annotations, err = p.recommendationProcessor.Apply(vpa.Status.Recommendation, vpa.Spec.ResourcePolicy, vpa.Status.Conditions, pod)
		if err != nil {
			klog.V(2).Infof("cannot process recommendation for pod %s", pod.Name)
			return nil, annotations, err
		}
	}
	containerLimitRange, err := p.limitsRangeCalculator.GetContainerLimitRangeItem(pod.Namespace)
	if err != nil {
		return nil, nil, fmt.Errorf("error getting containerLimitRange: %s", err)
	}
	var resourcePolicy *vpa_types.PodResourcePolicy
	if vpa.Spec.UpdatePolicy == nil || vpa.Spec.UpdatePolicy.UpdateMode == nil || *vpa.Spec.UpdatePolicy.UpdateMode != vpa_types.UpdateModeOff {
		resourcePolicy = vpa.Spec.ResourcePolicy
	}
	containerResources := GetContainersResources(pod, resourcePolicy, *recommendedPodResources, containerLimitRange, false, annotations, vpa.GetAnnotations(), p.enableQoS)
	return containerResources, annotations, nil
}
