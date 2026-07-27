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
func (b *BalancingNodeGroupSetProcessor) BalanceScaleUpBetweenGroups(autoscalingCtx *ca_context.AutoscalingContext, groups []cloudprovider.NodeGroup, newNodes int) ([]ScaleUpInfo, errors.AutoscalerError) {
	return b.balance(autoscalingCtx, groups, newNodes, nil)
}

// BalanceScaleUpBetweenGroupsWithQuota is BalanceScaleUpBetweenGroups, but consults reserver
// before assigning each node to a group, so that a quota pool shared by several groups is
// drawn down correctly across them as the fill progresses, rather than each group checking the
// same pool independently.
//
// The result is max-min fair for laminar quota structures: independent per-group quotas, or a
// single pool shared by every group passed in. When quota pools only partially overlap between
// groups (e.g. one pool covering {a,b} and another covering {b,c}), this is only a heuristic: it
// never violates a quota and is never worse than balancing without quota awareness, but it is
// not guaranteed to be optimal. It can be strictly dominated by a feasible allocation that avoids
// spending two pools on the same node, and it can sacrifice total throughput relative to an
// allocation that draws down a shared pool less evenly. Computing the optimal split for
// arbitrary overlapping quotas is a linear program and is out of scope here.
func (b *BalancingNodeGroupSetProcessor) BalanceScaleUpBetweenGroupsWithQuota(autoscalingCtx *ca_context.AutoscalingContext, groups []cloudprovider.NodeGroup, newNodes int, reserver NodeQuotaReserver) ([]ScaleUpInfo, errors.AutoscalerError) {
	return b.balance(autoscalingCtx, groups, newNodes, reserver)
}

// balanceEntry is the loop's scratch representation of a ScaleUpInfo being filled. frozen
// records whether this entry has been removed from consideration (at MaxSize, or refused by
// quota); it travels with the entry through the swaps below, so no side map keyed by group id
// is needed.
type balanceEntry struct {
	ScaleUpInfo
	frozen bool
}

// balance implements the fill loop shared by BalanceScaleUpBetweenGroups and
// BalanceScaleUpBetweenGroupsWithQuota. reserver is nil for the former.
func (b *BalancingNodeGroupSetProcessor) balance(autoscalingCtx *ca_context.AutoscalingContext, groups []cloudprovider.NodeGroup, newNodes int, reserver NodeQuotaReserver) ([]ScaleUpInfo, errors.AutoscalerError) {
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
	// Sort the node groups by (current size, id) and just loop over nodes adding
	// to the smallest group. If a group hits max size, or - when reserver is set -
	// its quota is refused, freeze it and remove it from the list (by moving it to
	// the start of the list and increasing startIndex).
	//
	// In each iteration we either allocate one node, or freeze one node group, so
	// this terminates in O(#nodes + #node groups) steps: every iteration either
	// decrements newNodes or increments startIndex. This is also why a quota
	// refusal must always freeze the group - returning false from the reserver
	// without freezing would spin forever re-trying the same group.
	//
	// Loop invariants (hold at the top of each iteration):
	// 1. i < startIndex -> scaleUpInfos[i] is frozen (at MaxSize, or refused by quota)
	// 2. i >= startIndex -> scaleUpInfos[i] is not frozen, i.e. scaleUpInfos[i].NewSize < scaleUpInfos[i].MaxSize
	// 3. startIndex <= currentIndex < len(scaleUpInfos), UNLESS newNodes has been
	//    fully allocated or every group has been frozen, in which case
	//    currentIndex == len(scaleUpInfos) right after the final freeze is expected,
	//    and the loop guard below stops before it is dereferenced.
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
	// Quota freezing breaks the old precondition that newNodes <= total capacity
	// implies currentIndex always stays in bounds: a group can be refused well
	// before it reaches MaxSize. The startIndex bound below is required to avoid
	// indexing past the end of scaleUpInfos once every group has been frozen.
	for newNodes > 0 && startIndex < len(scaleUpInfos) {
		currentInfo := &scaleUpInfos[currentIndex]

		if currentInfo.NewSize < currentInfo.MaxSize && (reserver == nil || reserver.ReserveNode(currentInfo.Group.Id())) {
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

		// Update currentIndex.
		// currentInfo is a pointer to the slot at currentIndex, not to a specific
		// group: when the branch above just froze and swapped, the read below
		// intentionally observes whatever group the swap moved INTO that slot
		// (i.e. the group that used to sit at startIndex), because that's the
		// still-active group we want to keep comparing and filling next.
		// If we froze a group in this loop currentIndex may be equal to startIndex-1,
		// in which case both branches of below if will make currentIndex == startIndex.
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

// CleanUp performs final clean up of processor state.
func (b *BalancingNodeGroupSetProcessor) CleanUp() {}

// Compile-time assertion that BalancingNodeGroupSetProcessor stays quota-aware, so a future
// refactor cannot silently demote production to the legacy, non-redistributing orchestrator path.
var _ QuotaAwareNodeGroupSetProcessor = &BalancingNodeGroupSetProcessor{}
