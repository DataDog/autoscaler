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

package nodegroupset

import (
	"sort"

	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	ca_context "k8s.io/autoscaler/cluster-autoscaler/context"
	"k8s.io/autoscaler/cluster-autoscaler/simulator/framework"
	"k8s.io/autoscaler/cluster-autoscaler/utils/errors"

	klog "k8s.io/klog/v2"
)

// BalancingNodeGroupSetProcessor tries to keep similar node groups balanced on scale-up.
type BalancingNodeGroupSetProcessor struct {
	Comparator NodeInfoComparator
}

// FindSimilarNodeGroups returns a list of NodeGroups similar to the given one using the
// BalancingNodeGroupSetProcessor's comparator function.
func (b *BalancingNodeGroupSetProcessor) FindSimilarNodeGroups(autoscalingCtx *ca_context.AutoscalingContext, nodeGroup cloudprovider.NodeGroup,
	nodeInfosForGroups map[string]*framework.NodeInfo) ([]cloudprovider.NodeGroup, errors.AutoscalerError) {

	result := []cloudprovider.NodeGroup{}
	nodeGroupId := nodeGroup.Id()
	nodeInfo, found := nodeInfosForGroups[nodeGroupId]
	if !found {
		return []cloudprovider.NodeGroup{}, errors.NewAutoscalerErrorf(
			errors.InternalError,
			"failed to find template node for node group %s",
			nodeGroupId)
	}
	for _, ng := range autoscalingCtx.CloudProvider.NodeGroups() {
		ngId := ng.Id()
		if ngId == nodeGroupId {
			continue
		}
		ngNodeInfo, found := nodeInfosForGroups[ngId]
		if !found {
			klog.Warningf("Failed to find nodeInfo for group %v", ngId)
			continue
		}
		comparator := b.Comparator
		if comparator == nil {
			klog.Fatal("BalancingNodeGroupSetProcessor comparator not set")
		}
		if comparator(nodeInfo, ngNodeInfo) {
			result = append(result, ng)
		}
	}
	return result, nil
}

// BalanceScaleUpBetweenGroups distributes a given number of nodes between
// given set of NodeGroups. The nodes are added to smallest group first, trying
// to make the group sizes as evenly balanced as possible.
//
// Returns ScaleUpInfos for groups that need to be resized.
//
// MaxSize of each group will be respected. If newNodes > total free capacity
// of all NodeGroups it will be capped to total capacity. In particular if all
// group already have MaxSize, empty list will be returned.
//
// If reserver is non-nil, it's consulted before each node is placed and can
// refuse a group, freezing it like a group at MaxSize; this lets a quota pool
// shared by several groups drain correctly as the fill progresses. The result
// is max-min fair for independent quotas or a single shared pool, but only a
// heuristic for partially-overlapping pools: never quota-violating, but not
// guaranteed optimal (the optimal split is a linear program, out of scope).
func (b *BalancingNodeGroupSetProcessor) BalanceScaleUpBetweenGroups(autoscalingCtx *ca_context.AutoscalingContext, groups []cloudprovider.NodeGroup, newNodes int, reserver func(groupID string) bool) ([]ScaleUpInfo, errors.AutoscalerError) {
	if len(groups) == 0 {
		return []ScaleUpInfo{}, errors.NewAutoscalerError(
			errors.InternalError, "Can't balance scale up between 0 groups")
	}

	// get all data from cloudprovider, build data structure
	scaleUpInfos := make([]balanceEntry, 0)
	totalCapacity := 0
	for _, ng := range groups {
		currentSize, err := ng.TargetSize()
		if err != nil {
			return []ScaleUpInfo{}, errors.NewAutoscalerErrorf(
				errors.CloudProviderError,
				"failed to get node group size: %v", err)
		}
		maxSize := ng.MaxSize()
		if currentSize == maxSize {
			// group already maxed, ignore it
			continue
		}
		if maxSize > currentSize {
			// we still have capacity to expand
			totalCapacity += (maxSize - currentSize)
		}
		scaleUpInfos = append(scaleUpInfos, balanceEntry{ScaleUpInfo: ScaleUpInfo{
			Group:       ng,
			CurrentSize: currentSize,
			NewSize:     currentSize,
			MaxSize:     maxSize,
		}})
	}
	if totalCapacity < newNodes {
		klog.V(2).Infof("Requested scale-up (%v) exceeds node group set capacity, capping to %v", newNodes, totalCapacity)
		newNodes = totalCapacity
	}

	// The actual balancing algorithm.
	// Sort the node groups by (current size, id) and loop over nodes, adding to
	// the smallest group. A group is frozen and removed from the list (swapped
	// to the front, advancing startIndex) once it hits MaxSize or - when
	// reserver is set - has a node refused. A refusal must always freeze the
	// group, or the reserver would be re-asked forever with no progress.
	//
	// Terminates in O(#nodes + #node groups) steps: every iteration either
	// allocates a node or freezes a group.
	//
	// Loop invariants (hold at the top of each iteration):
	// 1. i < startIndex -> scaleUpInfos[i] is frozen
	// 2. i >= startIndex -> scaleUpInfos[i].NewSize < scaleUpInfos[i].MaxSize
	// 3. startIndex <= currentIndex < len(scaleUpInfos), except once every group
	//    is frozen or newNodes is exhausted, when currentIndex may equal
	//    len(scaleUpInfos); the loop guard below stops before that's dereferenced
	// 4. currentIndex <= i < j -> scaleUpInfos[i].NewSize <= scaleUpInfos[j].NewSize
	// 5. startIndex <= i < j < currentIndex -> scaleUpInfos[i].NewSize == scaleUpInfos[j].NewSize
	// 6. startIndex <= i < currentIndex <= j -> scaleUpInfos[i].NewSize <= scaleUpInfos[j].NewSize + 1
	sort.Slice(scaleUpInfos, func(i, j int) bool {
		if scaleUpInfos[i].CurrentSize != scaleUpInfos[j].CurrentSize {
			return scaleUpInfos[i].CurrentSize < scaleUpInfos[j].CurrentSize
		}
		return scaleUpInfos[i].Group.Id() < scaleUpInfos[j].Group.Id()
	})
	startIndex := 0
	currentIndex := 0
	// startIndex bounds the loop because quota freezing, unlike hitting MaxSize,
	// can remove every group before newNodes reaches zero.
	for newNodes > 0 && startIndex < len(scaleUpInfos) {
		currentInfo := &scaleUpInfos[currentIndex]

		if currentInfo.NewSize < currentInfo.MaxSize && (reserver == nil || reserver(currentInfo.Group.Id())) {
			// Add a node to group on currentIndex
			currentInfo.NewSize++
			newNodes--
		} else {
			// Group on currentIndex is frozen - either full, or its quota was just
			// refused. Remove it from the array. Removing is done by swapping the
			// group with the first group still in array and moving the start of
			// the array. Every group between startIndex and currentIndex has the
			// same size, so we can swap without breaking ordering.
			currentInfo.frozen = true
			scaleUpInfos[startIndex], scaleUpInfos[currentIndex] = scaleUpInfos[currentIndex], scaleUpInfos[startIndex]
			startIndex++
		}

		// Update currentIndex. currentInfo points at the slot, not a specific
		// group, so after a freeze+swap it refers to whichever group the swap
		// moved in - the still-active one we want to compare next. If a group
		// was frozen, currentIndex may be startIndex-1, in which case both
		// branches below set currentIndex = startIndex.
		if currentIndex < len(scaleUpInfos)-1 && currentInfo.NewSize > scaleUpInfos[currentIndex+1].NewSize {
			// Next group has exactly one less node, than current one.
			// We will increase it in next iteration.
			currentIndex++
		} else {
			// We reached end of array, or a group larger than the current one.
			// All groups from startIndex to currentIndex have the same size.
			// So we're moving to the beginning of array to loop over all of
			// them once again.
			currentIndex = startIndex
		}
	}

	// Filter out groups that haven't changed size
	result := make([]ScaleUpInfo, 0)
	for _, info := range scaleUpInfos {
		if info.NewSize != info.CurrentSize {
			result = append(result, info.ScaleUpInfo)
		}
	}

	return result, nil
}

// balanceEntry is the loop's scratch representation of a ScaleUpInfo being filled. frozen
// records whether this entry has been removed from consideration (at MaxSize, or refused by
// quota); it travels with the entry through the swaps below, so no side map keyed by group id
// is needed.
type balanceEntry struct {
	ScaleUpInfo
	frozen bool
}

// CleanUp performs final clean up of processor state.
func (b *BalancingNodeGroupSetProcessor) CleanUp() {}
