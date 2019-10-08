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

package volume

import (
	"time"

	apiv1 "k8s.io/api/core/v1"
	klog "k8s.io/klog/v2"
)

const (
	DatadogLocalStorageLabel = "nodegroups.datadoghq.com/local-storage"
)

func getUnreadyNodeCopy(node *apiv1.Node) *apiv1.Node {
	newNode := node.DeepCopy()
	newReadyCondition := apiv1.NodeCondition{
		Type:               apiv1.NodeReady,
		Status:             apiv1.ConditionFalse,
		LastTransitionTime: node.CreationTimestamp,
	}
	newNodeConditions := []apiv1.NodeCondition{newReadyCondition}
	for _, condition := range newNode.Status.Conditions {
		if condition.Type != apiv1.NodeReady {
			newNodeConditions = append(newNodeConditions, condition)
		}
	}
	newNode.Status.Conditions = newNodeConditions
	return newNode
}

func isReady(po *apiv1.Pod) bool {
	for _, condition := range po.Status.Conditions {
		if condition.Type != apiv1.PodReady {
			continue
		}
		if condition.Status != apiv1.ConditionTrue {
			continue
		}
		return time.Now().Sub(condition.LastTransitionTime.Time) > time.Second*90 // TODO: wait for the pv to be bound then bound to the pod in something else
	}
	return false
}

func FilterOutNodesWithUnreadyLocalVolume(pods []*apiv1.Pod, allNodes, readyNodes []*apiv1.Node) ([]*apiv1.Node, []*apiv1.Node) {
	localVolumePods := make(map[string]struct{})
	for _, po := range pods {
		if po.Namespace != "local-volume-provisioner" {
			continue
		}
		if !isReady(po) {
			continue
		}
		localVolumePods[po.Spec.NodeName] = struct{}{}
	}

	var newAllNodes []*apiv1.Node
	var newReadyNodes []*apiv1.Node
	nodesWithUnreadyVolumes := make(map[string]*apiv1.Node)
	for _, node := range readyNodes {
		// Nodes offering local storage shouldn't be considered ready until they run a local-volume-provisioner pod.
		_, isReady := localVolumePods[node.Name]
		if hasLocalStorage, ok := node.GetLabels()[DatadogLocalStorageLabel]; ok && hasLocalStorage == "false" {
			isReady = true
		}
		if isReady {
			newReadyNodes = append(newReadyNodes, node)
		} else {
			klog.V(0).Infof("Overriding status of node %v, which seems to have unready local volume", node.Name)
			nodesWithUnreadyVolumes[node.Name] = getUnreadyNodeCopy(node)
		}
	}
	// Override any node with unready volume with its "unready" copy
	for _, node := range allNodes {
		if newNode, found := nodesWithUnreadyVolumes[node.Name]; found {
			newAllNodes = append(newAllNodes, newNode)
		} else {
			newAllNodes = append(newAllNodes, node)
		}
	}
	return newAllNodes, newReadyNodes
}
