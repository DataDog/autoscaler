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

package pods

import (
	"k8s.io/autoscaler/cluster-autoscaler/core/podlistprocessor"
	"k8s.io/autoscaler/cluster-autoscaler/processors/pods"
	"k8s.io/autoscaler/cluster-autoscaler/simulator/framework"
)

// NewFilteringPodListProcessor returns an aggregated podlist
// processor, which wraps and sequentially runs other sub-processors.
// That's a slight modification of the orignal NewDefaultPodListProcessor()
// (from core/podlistprocessor/pod_list_processor.go) with NewTransformLocalData()
// processor on top, to support local-data persistent volumes.
func NewFilteringPodListProcessor(nodeFilter func(*framework.NodeInfo) bool) *pods.CombinedPodListProcessor {
	return pods.NewCombinedPodListProcessor([]pods.PodListProcessor{
		NewTransformLocalData(),
		NewTransformDataNodes(),
		NewFilterOutLongPending(),
		podlistprocessor.NewClearTPURequestsPodListProcessor(),
		podlistprocessor.NewFilterOutExpendablePodListProcessor(),
		podlistprocessor.NewCurrentlyDrainedNodesPodListProcessor(),
		podlistprocessor.NewFilterOutSchedulablePodListProcessor(nodeFilter),
		podlistprocessor.NewFilterOutDaemonSetPodListProcessor(),
	})
}
