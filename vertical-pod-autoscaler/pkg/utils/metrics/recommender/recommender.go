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

// Package recommender (aka metrics_recommender) - code for metrics of VPA Recommender
package recommender

import (
	"fmt"
	v12 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog"
	"strings"
	"time"

	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"

	"github.com/DataDog/datadog-go/v5/statsd"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/metrics"
)

const (
	metricsNamespace            = metrics.TopMetricsNamespace + "recommender"
	metricObjectCount           = "objects_count"
	metricVpaMatchedCount       = "matched_pod_count"
	metricVpaUnmatchedCount     = "unmatched_pod_count"
	metricPodDiffCores          = "pod_diff_cores"
	metricPodDiffMib            = "pod_diff_mib"
	metricContainerDiffCores    = "container_diff_cores"
	metricContainerDiffMib      = "container_diff_mib"
	metricRecommendationLatency = "recommendation_latency_seconds"
	metricAggContainerStates    = "aggregate_container_states_count"
)

var (
	modes = []string{string(vpa_types.UpdateModeOff), string(vpa_types.UpdateModeInitial), string(vpa_types.UpdateModeRecreate), string(vpa_types.UpdateModeAuto)}
)

type apiVersion string

const (
	v1beta1 apiVersion = "v1beta1"
	v1beta2 apiVersion = "v1beta2"
	v1      apiVersion = "v1"
)

var (
	statsdClient statsd.ClientInterface
	labelsToSave map[string]int

	matchedPods   map[string]int
	unmatchedPods map[string]int

	podKeys map[string][]string

	// Keep function latency a prometheus metric as its exposed API is used in more than the recommender.
	functionLatency = metrics.CreateExecutionTimeMetric(metricsNamespace,
		"Time spent in various parts of VPA Recommender main loop.")
)

type objectCounterKey struct {
	mode              string
	has               bool
	matchesPods       bool
	apiVersion        apiVersion
	unsupportedConfig bool
}

// ObjectCounter helps split all VPA objects into buckets
type ObjectCounter struct {
	cnt map[objectCounterKey]int
}

// Register initializes all metrics for VPA Recommender
func Register(client statsd.ClientInterface, saveLabels []string) {
	statsdClient = client
	labelsToSave = map[string]int{
		"pod_name":       1,
		"container_name": 1,
		"pod_namespace":  1,
	}
	for _, label := range saveLabels {
		labelsToSave[label] = 1
	}
}

// NewExecutionTimer provides a timer for Recommender's RunOnce execution
func NewExecutionTimer() *metrics.ExecutionTimer {
	return metrics.NewExecutionTimer(functionLatency)
}

// ObserveRecommendationLatency observes the time it took for the first recommendation to appear
func ObserveRecommendationLatency(created time.Time) {
	err := statsdClient.Histogram(metricRecommendationLatency, time.Since(created).Seconds(), nil, 1)
	if err != nil {
		klog.Errorf("Failed to write metric %s: %v", metricRecommendationLatency, err)
	}

}

// RecordAggregateContainerStatesCount records the number of containers being tracked by the recommender
func RecordAggregateContainerStatesCount(statesCount int) {
	err := statsdClient.Gauge(metricAggContainerStates, float64(statesCount), nil, 1.0)
	if err != nil {
		klog.Errorf("Failed to write metric %s: %v", metricAggContainerStates, err)
	}
}

func saveLabels(set labels.Labels) string {
	var saved strings.Builder
	tags := make([]string, 0, len(labelsToSave))
	for label := range labelsToSave {
		if set.Has(label) {
			value := label + ":" + set.Get(label)
			saved.WriteString(value + ";")
			tags = append(tags, value)
		}
	}
	result := saved.String()
	if len(podKeys[result]) == 0 {
		podKeys[result] = tags
	}
	return result
}

func concatLabels(first labels.Labels, second labels.Labels) labels.Labels {
	result := make(labels.Set)
	if first == nil {
		first = make(labels.Set)
	}
	if second == nil {
		second = make(labels.Set)
	}
	for k := range labelsToSave {
		if first.Has(k) {
			result[k] = first.Get(k)
		} else if second.Has(k) {
			result[k] = second.Get(k)
		}
	}
	return result
}

func diffResources(target v12.ResourceList, request v12.ResourceList) v12.ResourceList {
	cpu := target[v12.ResourceCPU].DeepCopy()
	mem := target[v12.ResourceMemory].DeepCopy()
	cpu.Sub(request[v12.ResourceCPU])
	mem.Sub(request[v12.ResourceMemory])
	return v12.ResourceList{v12.ResourceCPU: cpu, v12.ResourceMemory: mem}
}

func sumResources(lhs v12.ResourceList, rhs v12.ResourceList) v12.ResourceList {
	cpu := lhs[v12.ResourceCPU].DeepCopy()
	mem := lhs[v12.ResourceMemory].DeepCopy()
	cpu.Add(rhs[v12.ResourceCPU])
	mem.Add(rhs[v12.ResourceMemory])
	return v12.ResourceList{v12.ResourceCPU: cpu, v12.ResourceMemory: mem}
}

// StartScan marks the start of a set of connected calls of the recording methods RecordUnmatchedPod, RecordMatchedPod,
// RecordPodRequestDiff, and RecordContainerRequestDiff.  As they build a singular value-set, we initialize here and
// publish the metrics in FinishScan
func StartScan() {
	matchedPods = make(map[string]int)
	unmatchedPods = make(map[string]int)
	podKeys = make(map[string][]string)
}

// RecordUnmatchedPod records that a pod with the given labels wasn't matched.
func RecordUnmatchedPod(spec labels.Set) {
	unmatchedPods[saveLabels(spec)] += 1
}

// RecordMatchedPod records that a pod with the given labels was matched.
func RecordMatchedPod(spec labels.Set, vpa labels.Set) {
	matchedPods[saveLabels(concatLabels(spec, vpa))] += 1
}

// RecordPodRequestDiff records a pod's difference between its requests and the recommendation target.
func RecordPodRequestDiff(podNS string, podName string, podLabels labels.Labels,
	vpaLabels labels.Labels, target map[string]v12.ResourceList, request map[string]v12.ResourceList) {
	extraMetadata := map[string]string{
		"pod_name":      podName,
		"pod_namespace": podNS,
	}
	var accumulator v12.ResourceList
	for containerName := range target {
		delta := diffResources(target[containerName], request[containerName])
		accumulator = sumResources(accumulator, delta)
	}
	key := saveLabels(concatLabels(concatLabels(labels.Set(extraMetadata), podLabels), vpaLabels))
	cores := accumulator[v12.ResourceCPU]
	mibs := accumulator[v12.ResourceMemory]
	err := statsdClient.Histogram(metricPodDiffCores,
		float64(cores.MilliValue())/1000.0, podKeys[key], 1.0)
	if err != nil {
		klog.Errorf("Failed to write metric %s: %v", metricPodDiffCores, err)
	}
	err = statsdClient.Histogram(metricPodDiffMib,
		float64(mibs.ScaledValue(resource.Mega)), podKeys[key], 1.0)
	if err != nil {
		klog.Errorf("Failed to write metric %s: %v", metricPodDiffMib, err)
	}
}

// RecordContainerRequestDiff records a container's difference between its requests and the recommendation target.
func RecordContainerRequestDiff(containerName string, podNS string, podName string,
	podLabels labels.Labels, vpaLabels labels.Labels, target v12.ResourceList, request v12.ResourceList) {
	extraMetadata := map[string]string{
		"container_name": containerName,
		"pod_name":       podName,
		"pod_namespace":  podNS,
	}
	delta := diffResources(target, request)
	key := saveLabels(concatLabels(concatLabels(labels.Set(extraMetadata), podLabels), vpaLabels))
	cores := delta[v12.ResourceCPU]
	mibs := delta[v12.ResourceMemory]

	err := statsdClient.Histogram(metricContainerDiffCores,
		float64(cores.MilliValue())/1000.0, podKeys[key], 1.0)
	if err != nil {
		klog.Errorf("Failed to write metric %s: %v", metricContainerDiffCores, err)
	}
	err = statsdClient.Histogram(metricContainerDiffMib,
		float64(mibs.ScaledValue(resource.Mega)), podKeys[key], 1.0)
	if err != nil {
		klog.Errorf("Failed to write metric %s: %v", metricContainerDiffMib, err)
	}
}

// FinishScan marks the end of calls started with StartScan.  It published metrics derived
// from those calls.
func FinishScan() {
	for tagKey, count := range matchedPods {
		err := statsdClient.Gauge(metricVpaMatchedCount, float64(count), podKeys[tagKey], 1.0)
		if err != nil {
			klog.Errorf("Failed to write metric %s: %v", metricVpaMatchedCount, err)
		}
	}
	for tagKey, count := range unmatchedPods {
		err := statsdClient.Gauge(metricVpaUnmatchedCount, float64(count), podKeys[tagKey], 1.0)
		if err != nil {
			klog.Errorf("Failed to write metric %s: %v", metricVpaUnmatchedCount, err)
		}
	}
	err := statsdClient.Flush()
	if err != nil {
		klog.Errorf("Failed flushing to client: %v", err)
	}
}

// NewObjectCounter creates a new helper to split VPA objects into buckets
func NewObjectCounter() *ObjectCounter {
	obj := ObjectCounter{
		cnt: make(map[objectCounterKey]int),
	}

	// initialize with empty data so we can clean stale gauge values in Observe
	for _, m := range modes {
		for _, h := range []bool{false, true} {
			for _, api := range []apiVersion{v1beta1, v1beta2, v1} {
				for _, mp := range []bool{false, true} {
					for _, uc := range []bool{false, true} {
						obj.cnt[objectCounterKey{
							mode:              m,
							has:               h,
							apiVersion:        api,
							matchesPods:       mp,
							unsupportedConfig: uc,
						}] = 0
					}
				}
			}
		}
	}

	return &obj
}

// Add updates the helper state to include the given VPA object
func (oc *ObjectCounter) Add(updateMode *vpa_types.UpdateMode, isV1Beta1API bool, hasRecommendation bool,
	matchedPods bool, unsupportedConfig bool) {
	mode := string(vpa_types.UpdateModeAuto)
	if updateMode != nil && string(*updateMode) != "" {
		mode = string(*updateMode)
	}
	// TODO: Maybe report v1 version as well.
	api := v1beta2
	if isV1Beta1API {
		api = v1beta1
	}

	key := objectCounterKey{
		mode:              mode,
		has:               hasRecommendation,
		apiVersion:        api,
		matchesPods:       matchedPods,
		unsupportedConfig: unsupportedConfig,
	}
	oc.cnt[key]++
}

func labelsFor(oc objectCounterKey) []string {
	return []string{
		fmt.Sprintf("mode:%v", oc.mode),
		fmt.Sprintf("has_recommendation:%v", oc.has),
		fmt.Sprintf("api:%v", oc.apiVersion),
		fmt.Sprintf("matches_pods:%v", oc.matchesPods),
		fmt.Sprintf("unsupported_config:%v", oc.unsupportedConfig),
	}
}

// Observe passes all the computed bucket values to metrics
func (oc *ObjectCounter) Observe() {
	klog.Infof("Observe(): Writing out %d key/value pairs for %s", len(oc.cnt), metricObjectCount)
	for k, v := range oc.cnt {
		err := statsdClient.Gauge(metricObjectCount, float64(v), labelsFor(k), 1.0)
		if err != nil {
			klog.Errorf("Failed to write metric %s: %v", metricObjectCount, err)
		}
	}
	err := statsdClient.Flush()
	if err != nil {
		klog.Errorf("Failed to flush metrics: %v", err)
	}
}
