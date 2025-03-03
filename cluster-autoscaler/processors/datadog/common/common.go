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

package common

import (
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"
)

const (
	// DatadogLocalStorageLabel is "true" on nodes offering local storage
	DatadogLocalStorageLabel = "nodegroups.datadoghq.com/local-storage"
	// DatadogLocalStorageCapacityLabel is storing the amount of local storage a new node will have
	// e.g. nodegroups.datadoghq.com/local-storage-capacity=100Gi
	DatadogLocalStorageCapacityLabel = "nodegroups.datadoghq.com/local-storage-capacity"

	// DatadogLocalDataExistsResource is a virtual resource placed on new or future
	// nodes offering local storage, and currently injected as requests on
	// Pending pods having a PVC for local-data volumes.
	// This resource will always be 1 unit, since there is only 1 local-data volume per node
	DatadogLocalDataExistsResource apiv1.ResourceName = "storageclass/local-data"

	// DatadogLocalStorageResource is a virtual resource placed on new or future
	// nodes offering local storage, and currently injected as requests on
	// Pending pods having a PVC for local-storage volumes.
	// This is similar to DatadogLocalDataExistsResource, but this resource will have the actual amount of storage available on a node
	DatadogLocalStorageResource apiv1.ResourceName = "node.datadoghq.com/local-storage"
)

var (
	// DatadogLocalDataQuantity is used to ensure pods that have PVCs that request local-data will not be bin packed
	// since there is only 1 local-data volume per node
	DatadogLocalDataQuantity = resource.NewQuantity(1, resource.DecimalSI)
)

// NodeHasLocalData returns true if the node holds a local-storage:true label
func NodeHasLocalData(node *apiv1.Node) bool {
	if node == nil {
		return false
	}
	value, ok := node.GetLabels()[DatadogLocalStorageLabel]
	return ok && value == "true"
}

// ReducedNodeInfo is a reduced NodeInfo interface mean to requires just what's
// needed by SetNodeLocalDataResource(), and remain compatible with both
// k8s.io/kubernetes/pkg/scheduler/framework NodeInfo object and with
// k8s.io/kube-scheduler/framework NodeInfo interface.
type ReducedNodeInfo interface {
	Node() *apiv1.Node
	SetNode(node *apiv1.Node)
}

// SetNodeLocalDataResource updates a NodeInfo with the DatadogLocalDataResource resource
func SetNodeLocalDataResource(nodeInfo ReducedNodeInfo) {
	node := nodeInfo.Node()
	if node == nil {
		return
	}

	if node.Status.Allocatable == nil {
		node.Status.Allocatable = apiv1.ResourceList{}
	}
	if node.Status.Capacity == nil {
		node.Status.Capacity = apiv1.ResourceList{}
	}

	// Set the local-data resource to 1 unit
	node.Status.Capacity[DatadogLocalDataExistsResource] = DatadogLocalDataQuantity.DeepCopy()
	node.Status.Allocatable[DatadogLocalDataExistsResource] = DatadogLocalDataQuantity.DeepCopy()

	// Set the local-storage resource to the value of the local-storage-capacity label
	capacity := node.Labels[DatadogLocalStorageCapacityLabel]
	capacityResource, err := resource.ParseQuantity(capacity)
	if err != nil {
		klog.Warningf("failed to parse local storage capacity information (%s) for node (%s): %v", capacity, node.Name, err)
		// fallback to default if something went wrong with the label value
		capacityResource = DatadogLocalDataQuantity.DeepCopy()
	}

	node.Status.Capacity[DatadogLocalStorageResource] = capacityResource.DeepCopy()
	node.Status.Allocatable[DatadogLocalStorageResource] = capacityResource.DeepCopy()

	// Even though we get a pointer to the original node (and the update above
	// changes it in place), we still need to call SetNode() in order to trigger
	// an update of the NodeInfo concrete implementation: the implem provided by
	// k8s.io/kubernetes/pkg/scheduler/framework maintains an Allocatable field
	// at SetNode() time, but we don't have acess to that field as we only have
	// access to a reduced NodeInfo interface returned by ClusterSnapshot.List().
	// We can't RemoveNode() out of extra caution as we did previously (with older
	// Autoscaler codebases when ClusterSnapshot.List() returned concrete nodeInfo
	// implementations, because the NodeInfo _interface_ has no RemoveNode() method.
	nodeInfo.SetNode(node)
}
