/*
Copyright 2017 The Kubernetes Authors.

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

package logic

import (
	"flag"
	"fmt"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/annotations"
	"math"
	"strings"

	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
	"k8s.io/klog/v2"
)

var (
	safetyMarginFraction = flag.Float64("recommendation-margin-fraction", 0.15, `Fraction of usage added as the safety margin to the recommended request`)
	podMinCPUMillicores  = flag.Float64("pod-recommendation-min-cpu-millicores", 25, `Minimum CPU recommendation for a pod`)
	podMinMemoryMb       = flag.Float64("pod-recommendation-min-memory-mb", 250, `Minimum memory recommendation for a pod`)
)

const (
	annotationObjectType          = "vpa.datadoghq.com/type"
	annotationPreferredUpdateMode = "vpa.datadoghq.com/updateMode"
	annotationQosEnable           = "vpa.datadoghq.com/qosEnable"

	objectTypeDaemonSet = "DaemonSet"
)

// PodResourceRecommender computes resource recommendation for a Vpa object.
type PodResourceRecommender interface {
	GetRecommendedPodResources(containerNameToAggregateStateMap model.ContainerNameToAggregateStateMap, podAnnotations map[string]string) RecommendedPodResources
}

// RecommendedPodResources is a Map from container name to recommended resources.
type RecommendedPodResources map[string]RecommendedContainerResources

// RecommendedContainerResources is the recommendation of resources for a
// container.
type RecommendedContainerResources struct {
	// Recommended optimal amount of resources.
	Target model.Resources
	// Recommended minimum amount of resources.
	LowerBound model.Resources
	// Recommended maximum amount of resources.
	UpperBound model.Resources
}

type annotationsPredicate func(map[string]string) bool

type podResourceRecommenderEntry struct {
	predicate           annotationsPredicate
	targetEstimator     ResourceEstimator
	lowerBoundEstimator ResourceEstimator
	upperBoundEstimator ResourceEstimator
}

type podResourceRecommender struct {
	entries []podResourceRecommenderEntry
}

func acceptAll(map[string]string) bool { return true }

func acceptQoS(annotations map[string]string) bool {
	// The QoS enable flag has primary control.  Then we go by type.
	if qosEnable, qosOk := annotations[annotationQosEnable]; qosOk {
		qosEnable = strings.ToLower(qosEnable)
		if qosEnable == "false" {
			return false
		}
		// Only useful when the other conditions below might disable QoS
		if qosEnable == "true" {
			return true
		}
	}
	if obType, otypeOk := annotations[annotationObjectType]; otypeOk {
		if obType == objectTypeDaemonSet {
			return false
		}
	}
	// QoS by default.
	return true
}

func (r *podResourceRecommender) recommenderForAnnotations(podAnnotations map[string]string) podResourceRecommenderEntry {
	for _, ent := range r.entries {
		if ent.predicate(podAnnotations) {
			return ent
		}
	}
	panic(fmt.Sprintf("No recommender found for annotation set %+v, the last one should always apply!", podAnnotations))
}

func (r *podResourceRecommender) GetRecommendedPodResources(containerNameToAggregateStateMap model.ContainerNameToAggregateStateMap,
	vpaAnnotations map[string]string) RecommendedPodResources {
	var recommendation = make(RecommendedPodResources)
	if len(containerNameToAggregateStateMap) == 0 {
		klog.V(6).Infof("Recommendation: No keys in state map.")
		return recommendation
	}

	fraction := 1.0 / float64(len(containerNameToAggregateStateMap))
	minResources := model.Resources{
		model.ResourceCPU:    model.ScaleResource(model.CPUAmountFromCores(*podMinCPUMillicores*0.001), fraction),
		model.ResourceMemory: model.ScaleResource(model.MemoryAmountFromBytes(*podMinMemoryMb*1024*1024), fraction),
	}

	rec := r.recommenderForAnnotations(vpaAnnotations)
	recommender := &podResourceRecommenderEntry{
		acceptAll,
		WithMinResources(minResources, rec.targetEstimator),
		WithMinResources(minResources, rec.lowerBoundEstimator),
		WithMinResources(minResources, rec.upperBoundEstimator),
	}

	extensions, err := annotations.ParseDatadogExtensions(vpaAnnotations)
	if err != nil {
		klog.V(2).Infof("Failed parsing vpa annotations on %+v: %v", vpaAnnotations, err)
	}
	options := ResourceEstimatorOptions{Extensions: extensions}
	for containerName, aggregatedContainerState := range containerNameToAggregateStateMap {
		recommendation[containerName] = recommender.estimateContainerResources(aggregatedContainerState, options)
	}

	numContainers, numSamples := 0, 0
	for name, mp := range containerNameToAggregateStateMap {
		numContainers += 1
		numSamples += mp.TotalSamplesCount
		klog.V(4).Infof("Recommendation[%s]: We have %d samples", name, mp.TotalSamplesCount)
	}
	klog.V(3).Infof("Recommendation: For %d containers we have %d samples", numContainers, numSamples)

	return recommendation
}

// Takes AggregateContainerState and returns a container recommendation.
func (r *podResourceRecommenderEntry) estimateContainerResources(s *model.AggregateContainerState,
	options ResourceEstimatorOptions) RecommendedContainerResources {
	return RecommendedContainerResources{
		FilterControlledResources(r.targetEstimator.GetResourceEstimation(s, options), s.GetControlledResources()),
		FilterControlledResources(r.lowerBoundEstimator.GetResourceEstimation(s, options), s.GetControlledResources()),
		FilterControlledResources(r.upperBoundEstimator.GetResourceEstimation(s, options), s.GetControlledResources()),
	}
}

// FilterControlledResources returns estimations from 'estimation' only for resources present in 'controlledResources'.
func FilterControlledResources(estimation model.Resources, controlledResources []model.ResourceName) model.Resources {
	result := make(model.Resources)
	for _, resource := range controlledResources {
		if value, ok := estimation[resource]; ok {
			result[resource] = value
		}
	}
	return result
}

// CreatePodResourceRecommender returns the primary recommender.
func CreatePodResourceRecommender(cpuQos bool, percentile float64) PodResourceRecommender {
	targetCPUPercentile := percentile
	lowerBoundCPUPercentile := 0.5
	upperBoundCPUPercentile := math.Max(0.995, percentile)

	targetMemoryPeaksPercentile := percentile
	lowerBoundMemoryPeaksPercentile := 0.5
	upperBoundMemoryPeaksPercentile := math.Max(0.995, percentile)

	targetEstimator := NewPercentileEstimator(targetCPUPercentile, targetMemoryPeaksPercentile)
	lowerBoundEstimator := NewPercentileEstimator(lowerBoundCPUPercentile, lowerBoundMemoryPeaksPercentile)
	upperBoundEstimator := NewPercentileEstimator(upperBoundCPUPercentile, upperBoundMemoryPeaksPercentile)

	targetEstimator = WithMargin(*safetyMarginFraction, targetEstimator)
	lowerBoundEstimator = WithMargin(*safetyMarginFraction, lowerBoundEstimator)
	upperBoundEstimator = WithMargin(*safetyMarginFraction, upperBoundEstimator)

	// Apply confidence multiplier to the upper bound estimator. This means
	// that the updater will be less eager to evict pods with short history
	// in order to reclaim unused resources.
	// Using the confidence multiplier 1 with exponent +1 means that
	// the upper bound is multiplied by (1 + 1/history-length-in-days).
	// See estimator.go to see how the history length and the confidence
	// multiplier are determined. The formula yields the following multipliers:
	// No history     : *INF  (do not force pod eviction)
	// 12h history    : *3    (force pod eviction if the request is > 3 * upper bound)
	// 24h history    : *2
	// 1 week history : *1.14
	upperBoundEstimator = WithConfidenceMultiplier(1.0, 1.0, upperBoundEstimator)

	// Apply confidence multiplier to the lower bound estimator. This means
	// that the updater will be less eager to evict pods with short history
	// in order to provision them with more resources.
	// Using the confidence multiplier 0.001 with exponent -2 means that
	// the lower bound is multiplied by the factor (1 + 0.001/history-length-in-days)^-2
	// (which is very rapidly converging to 1.0).
	// See estimator.go to see how the history length and the confidence
	// multiplier are determined. The formula yields the following multipliers:
	// No history   : *0   (do not force pod eviction)
	// 5m history   : *0.6 (force pod eviction if the request is < 0.6 * lower bound)
	// 30m history  : *0.9
	// 60m history  : *0.95
	lowerBoundEstimator = WithConfidenceMultiplier(0.001, -2.0, lowerBoundEstimator)

	baseEstimator := &podResourceRecommenderEntry{
		acceptAll,
		targetEstimator,
		lowerBoundEstimator,
		upperBoundEstimator,
	}

	// If we have QoS enabled, put it ahead of the base recommender on the list, and use the right predicate to
	// make it apply only when it matters.
	if cpuQos {
		qosEstimator := &podResourceRecommenderEntry{
			acceptQoS,
			WithQosRoundingEstimator(targetEstimator),
			WithQosRoundingEstimator(lowerBoundEstimator),
			WithQosRoundingEstimator(upperBoundEstimator),
		}
		return &podResourceRecommender{
			[]podResourceRecommenderEntry{*qosEstimator, *baseEstimator},
		}
	}
	return &podResourceRecommender{
		[]podResourceRecommenderEntry{*baseEstimator},
	}
}
