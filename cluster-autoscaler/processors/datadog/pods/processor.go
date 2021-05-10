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

package pods

import (
	"k8s.io/autoscaler/cluster-autoscaler/core/podlistprocessor"
	proc "k8s.io/autoscaler/cluster-autoscaler/processors/pods"
	"k8s.io/autoscaler/cluster-autoscaler/simulator/predicatechecker"
	schedulerframework "k8s.io/kubernetes/pkg/scheduler/framework"
)

// NewFilteringPodListProcessor returns an aggregated podlist processor
func NewFilteringPodListProcessor(predicateChecker predicatechecker.PredicateChecker, nodeFilter func(*schedulerframework.NodeInfo) bool) *proc.CombinedPodListProcessor {
	return proc.NewCombinedPodListProcessor([]proc.PodListProcessor{
		NewTransformLocalData(),
		NewFilterOutLongPending(),
		podlistprocessor.NewClearTPURequestsPodListProcessor(),
		podlistprocessor.NewFilterOutExpendablePodListProcessor(),
		podlistprocessor.NewCurrentlyDrainedNodesPodListProcessor(),
		podlistprocessor.NewFilterOutSchedulablePodListProcessor(predicateChecker, nodeFilter),
		podlistprocessor.NewFilterOutDaemonSetPodListProcessor(),
	})
}
