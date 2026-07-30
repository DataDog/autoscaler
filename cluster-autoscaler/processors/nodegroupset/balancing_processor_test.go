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

package nodegroupset

import (
	"testing"

	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	testprovider "k8s.io/autoscaler/cluster-autoscaler/cloudprovider/test"
	"k8s.io/autoscaler/cluster-autoscaler/config"
	ca_context "k8s.io/autoscaler/cluster-autoscaler/context"
	"k8s.io/autoscaler/cluster-autoscaler/simulator/framework"
	. "k8s.io/autoscaler/cluster-autoscaler/utils/test"

	"github.com/stretchr/testify/assert"
)

func buildBasicNodeGroups(autoscalingCtx *ca_context.AutoscalingContext) (*framework.NodeInfo, *framework.NodeInfo, *framework.NodeInfo) {
	n1 := BuildTestNode("n1", 1000, 1000)
	n2 := BuildTestNode("n2", 1000, 1000)
	n3 := BuildTestNode("n3", 2000, 2000)
	provider := testprovider.NewTestCloudProviderBuilder().Build()
	provider.AddNodeGroup("ng1", 1, 10, 1)
	provider.AddNodeGroup("ng2", 1, 10, 1)
	provider.AddNodeGroup("ng3", 1, 10, 1)
	provider.AddNode("ng1", n1)
	provider.AddNode("ng2", n2)
	provider.AddNode("ng3", n3)

	ni1 := framework.NewTestNodeInfo(n1)
	ni2 := framework.NewTestNodeInfo(n2)
	ni3 := framework.NewTestNodeInfo(n3)

	autoscalingCtx.CloudProvider = provider
	return ni1, ni2, ni3
}

func basicSimilarNodeGroupsTest(
	t *testing.T,
	autoscalingCtx *ca_context.AutoscalingContext,
	processor NodeGroupSetProcessor,
	ni1 *framework.NodeInfo,
	ni2 *framework.NodeInfo,
	ni3 *framework.NodeInfo,
) {
	nodeInfosForGroups := map[string]*framework.NodeInfo{
		"ng1": ni1, "ng2": ni2, "ng3": ni3,
	}

	ng1, _ := autoscalingCtx.CloudProvider.NodeGroupForNode(ni1.Node())
	ng2, _ := autoscalingCtx.CloudProvider.NodeGroupForNode(ni2.Node())
	ng3, _ := autoscalingCtx.CloudProvider.NodeGroupForNode(ni3.Node())

	similar, err := processor.FindSimilarNodeGroups(autoscalingCtx, ng1, nodeInfosForGroups)
	assert.NoError(t, err)
	assert.Equal(t, []cloudprovider.NodeGroup{ng2}, similar)

	similar, err = processor.FindSimilarNodeGroups(autoscalingCtx, ng2, nodeInfosForGroups)
	assert.NoError(t, err)
	assert.Equal(t, []cloudprovider.NodeGroup{ng1}, similar)

	similar, err = processor.FindSimilarNodeGroups(autoscalingCtx, ng3, nodeInfosForGroups)
	assert.NoError(t, err)
	assert.Equal(t, []cloudprovider.NodeGroup{}, similar)
}

func TestFindSimilarNodeGroups(t *testing.T) {
	autoscalingCtx := &ca_context.AutoscalingContext{}
	ni1, ni2, ni3 := buildBasicNodeGroups(autoscalingCtx)
	processor := NewDefaultNodeGroupSetProcessor([]string{}, config.NodeGroupDifferenceRatios{})
	basicSimilarNodeGroupsTest(t, autoscalingCtx, processor, ni1, ni2, ni3)
}

func TestFindSimilarNodeGroupsCustomLabels(t *testing.T) {
	autoscalingCtx := &ca_context.AutoscalingContext{}
	ni1, ni2, ni3 := buildBasicNodeGroups(autoscalingCtx)
	ni1.Node().Labels["example.com/ready"] = "true"
	ni2.Node().Labels["example.com/ready"] = "false"

	processor := NewDefaultNodeGroupSetProcessor([]string{"example.com/ready"}, config.NodeGroupDifferenceRatios{})
	basicSimilarNodeGroupsTest(t, autoscalingCtx, processor, ni1, ni2, ni3)
}

func TestFindSimilarNodeGroupsCustomComparator(t *testing.T) {
	autoscalingCtx := &ca_context.AutoscalingContext{}
	ni1, ni2, ni3 := buildBasicNodeGroups(autoscalingCtx)

	processor := &BalancingNodeGroupSetProcessor{
		Comparator: func(n1, n2 *framework.NodeInfo) bool {
			return (n1.Node().Name == "n1" && n2.Node().Name == "n2") ||
				(n1.Node().Name == "n2" && n2.Node().Name == "n1")
		},
	}
	basicSimilarNodeGroupsTest(t, autoscalingCtx, processor, ni1, ni2, ni3)
}

func TestBalanceSingleGroup(t *testing.T) {
	processor := NewDefaultNodeGroupSetProcessor([]string{}, config.NodeGroupDifferenceRatios{})
	autoscalingCtx := &ca_context.AutoscalingContext{}

	provider := testprovider.NewTestCloudProviderBuilder().Build()
	provider.AddNodeGroup("ng1", 1, 10, 1)

	// just one node
	scaleUpInfo, err := processor.BalanceScaleUpBetweenGroups(autoscalingCtx, provider.NodeGroups(), 1, nil)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(scaleUpInfo))
	assert.Equal(t, 2, scaleUpInfo[0].NewSize)

	// multiple nodes
	scaleUpInfo, err = processor.BalanceScaleUpBetweenGroups(autoscalingCtx, provider.NodeGroups(), 4, nil)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(scaleUpInfo))
	assert.Equal(t, 5, scaleUpInfo[0].NewSize)
}

func TestBalanceUnderMaxSize(t *testing.T) {
	processor := NewDefaultNodeGroupSetProcessor([]string{}, config.NodeGroupDifferenceRatios{})
	autoscalingCtx := &ca_context.AutoscalingContext{}

	provider := testprovider.NewTestCloudProviderBuilder().Build()
	provider.AddNodeGroup("ng1", 1, 10, 1)
	provider.AddNodeGroup("ng2", 1, 10, 3)
	provider.AddNodeGroup("ng3", 1, 10, 5)
	provider.AddNodeGroup("ng4", 1, 10, 5)

	// add a single node
	scaleUpInfo, err := processor.BalanceScaleUpBetweenGroups(autoscalingCtx, provider.NodeGroups(), 1, nil)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(scaleUpInfo))
	assert.Equal(t, 2, scaleUpInfo[0].NewSize)

	// add multiple nodes to single group
	scaleUpInfo, err = processor.BalanceScaleUpBetweenGroups(autoscalingCtx, provider.NodeGroups(), 2, nil)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(scaleUpInfo))
	assert.Equal(t, 3, scaleUpInfo[0].NewSize)

	// add nodes to groups of different sizes, divisible
	scaleUpInfo, err = processor.BalanceScaleUpBetweenGroups(autoscalingCtx, provider.NodeGroups(), 4, nil)
	assert.NoError(t, err)
	assert.Equal(t, 2, len(scaleUpInfo))
	assert.Equal(t, 4, scaleUpInfo[0].NewSize)
	assert.Equal(t, 4, scaleUpInfo[1].NewSize)
	assert.True(t, scaleUpInfo[0].Group.Id() == "ng1" || scaleUpInfo[1].Group.Id() == "ng1")
	assert.True(t, scaleUpInfo[0].Group.Id() == "ng2" || scaleUpInfo[1].Group.Id() == "ng2")

	// add nodes to groups of different sizes, non-divisible
	// we expect new sizes to be 4 and 5, doesn't matter which group gets how many
	scaleUpInfo, err = processor.BalanceScaleUpBetweenGroups(autoscalingCtx, provider.NodeGroups(), 5, nil)
	assert.NoError(t, err)
	assert.Equal(t, 2, len(scaleUpInfo))
	assert.Equal(t, 9, scaleUpInfo[0].NewSize+scaleUpInfo[1].NewSize)
	assert.True(t, scaleUpInfo[0].NewSize == 4 || scaleUpInfo[0].NewSize == 5)
	assert.True(t, scaleUpInfo[0].Group.Id() == "ng1" || scaleUpInfo[1].Group.Id() == "ng1")
	assert.True(t, scaleUpInfo[0].Group.Id() == "ng2" || scaleUpInfo[1].Group.Id() == "ng2")

	// add nodes to all groups, divisible
	scaleUpInfo, err = processor.BalanceScaleUpBetweenGroups(autoscalingCtx, provider.NodeGroups(), 10, nil)
	assert.NoError(t, err)
	assert.Equal(t, 4, len(scaleUpInfo))
	for _, info := range scaleUpInfo {
		assert.Equal(t, 6, info.NewSize)
	}
}

func TestBalanceHittingMaxSize(t *testing.T) {
	processor := NewDefaultNodeGroupSetProcessor([]string{}, config.NodeGroupDifferenceRatios{})
	autoscalingCtx := &ca_context.AutoscalingContext{}

	provider := testprovider.NewTestCloudProviderBuilder().Build()
	provider.AddNodeGroup("ng1", 1, 1, 1)
	provider.AddNodeGroup("ng2", 1, 3, 1)
	provider.AddNodeGroup("ng3", 1, 10, 3)
	provider.AddNodeGroup("ng4", 1, 7, 5)
	provider.AddNodeGroup("ng5", 1, 3, 6)
	groupsMap := make(map[string]cloudprovider.NodeGroup)
	for _, group := range provider.NodeGroups() {
		groupsMap[group.Id()] = group
	}

	getGroups := func(names ...string) []cloudprovider.NodeGroup {
		result := make([]cloudprovider.NodeGroup, 0)
		for _, n := range names {
			result = append(result, groupsMap[n])
		}
		return result
	}

	toMap := func(suiList []ScaleUpInfo) map[string]ScaleUpInfo {
		result := make(map[string]ScaleUpInfo, 0)
		for _, sui := range suiList {
			result[sui.Group.Id()] = sui
		}
		return result
	}

	// Just one maxed out group
	scaleUpInfo, err := processor.BalanceScaleUpBetweenGroups(autoscalingCtx, getGroups("ng1"), 1, nil)
	assert.NoError(t, err)
	assert.Equal(t, 0, len(scaleUpInfo))

	// Smallest group already maxed out, add one node
	scaleUpInfo, err = processor.BalanceScaleUpBetweenGroups(autoscalingCtx, getGroups("ng1", "ng2"), 1, nil)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(scaleUpInfo))
	assert.Equal(t, "ng2", scaleUpInfo[0].Group.Id())
	assert.Equal(t, 2, scaleUpInfo[0].NewSize)

	// Smallest group already maxed out, too many nodes (should cap to max capacity)
	scaleUpInfo, err = processor.BalanceScaleUpBetweenGroups(autoscalingCtx, getGroups("ng1", "ng2"), 5, nil)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(scaleUpInfo))
	assert.Equal(t, "ng2", scaleUpInfo[0].Group.Id())
	assert.Equal(t, 3, scaleUpInfo[0].NewSize)

	// First group maxes out before proceeding to next one
	scaleUpInfo, _ = processor.BalanceScaleUpBetweenGroups(autoscalingCtx, getGroups("ng2", "ng3"), 4, nil)
	assert.Equal(t, 2, len(scaleUpInfo))
	scaleUpMap := toMap(scaleUpInfo)
	assert.Equal(t, 3, scaleUpMap["ng2"].NewSize)
	assert.Equal(t, 5, scaleUpMap["ng3"].NewSize)

	// Last group maxes out before previous one
	scaleUpInfo, _ = processor.BalanceScaleUpBetweenGroups(autoscalingCtx, getGroups("ng2", "ng3", "ng4"), 9, nil)
	assert.Equal(t, 3, len(scaleUpInfo))
	scaleUpMap = toMap(scaleUpInfo)
	assert.Equal(t, 3, scaleUpMap["ng2"].NewSize)
	assert.Equal(t, 8, scaleUpMap["ng3"].NewSize)
	assert.Equal(t, 7, scaleUpMap["ng4"].NewSize)

	// Use all capacity, cap to max
	scaleUpInfo, _ = processor.BalanceScaleUpBetweenGroups(autoscalingCtx, getGroups("ng2", "ng3", "ng4"), 900, nil)
	assert.Equal(t, 3, len(scaleUpInfo))
	scaleUpMap = toMap(scaleUpInfo)
	assert.Equal(t, 3, scaleUpMap["ng2"].NewSize)
	assert.Equal(t, 10, scaleUpMap["ng3"].NewSize)
	assert.Equal(t, 7, scaleUpMap["ng4"].NewSize)

	// One node group exceeds max.
	scaleUpInfo, _ = processor.BalanceScaleUpBetweenGroups(autoscalingCtx, getGroups("ng2", "ng5"), 1, nil)
	assert.Equal(t, 1, len(scaleUpInfo))
	scaleUpMap = toMap(scaleUpInfo)
	assert.Equal(t, 2, scaleUpMap["ng2"].NewSize)
}

// quotaPool models a resource quota shared by a set of node groups, holding a
// remaining node budget. A group can be covered by more than one pool.
type quotaPool struct {
	groups    map[string]bool
	remaining int
}

func pool(remaining int, groups ...string) *quotaPool {
	g := make(map[string]bool, len(groups))
	for _, id := range groups {
		g[id] = true
	}
	return &quotaPool{groups: g, remaining: remaining}
}

// fakeReserver is a reserver test double backed by quotaPools. ReserveNode
// grants a node only if every pool covering the group has budget, and
// decrements them all together, mirroring how several real quotas would.
type fakeReserver struct {
	pools []*quotaPool
}

func newFakeReserver(pools ...*quotaPool) *fakeReserver {
	return &fakeReserver{pools: pools}
}

func (f *fakeReserver) covering(groupID string) []*quotaPool {
	var covering []*quotaPool
	for _, p := range f.pools {
		if p.groups[groupID] {
			covering = append(covering, p)
		}
	}
	return covering
}

func (f *fakeReserver) ReserveNode(groupID string) bool {
	covering := f.covering(groupID)
	for _, p := range covering {
		if p.remaining <= 0 {
			return false
		}
	}
	for _, p := range covering {
		p.remaining--
	}
	return true
}

// groupSpec describes a node group to build for the quota-aware balancing
// tests below: id, (min, max, initial size).
type groupSpec struct {
	id             string
	min, max, size int
}

// buildGroupsForQuotaTest builds a test cloud provider with one node group per
// spec, in the given order, and returns them as a []cloudprovider.NodeGroup
// suitable for passing straight to BalanceScaleUpBetweenGroups.
func buildGroupsForQuotaTest(specs []groupSpec) []cloudprovider.NodeGroup {
	provider := testprovider.NewTestCloudProviderBuilder().Build()
	for _, s := range specs {
		provider.AddNodeGroup(s.id, s.min, s.max, s.size)
	}
	groups := make([]cloudprovider.NodeGroup, 0, len(specs))
	for _, s := range specs {
		groups = append(groups, provider.GetNodeGroup(s.id))
	}
	return groups
}

func TestBalanceScaleUpBetweenGroupsWithQuota(t *testing.T) {
	processor := NewDefaultNodeGroupSetProcessor([]string{}, config.NodeGroupDifferenceRatios{}).(*BalancingNodeGroupSetProcessor)
	autoscalingCtx := &ca_context.AutoscalingContext{}

	tests := []struct {
		name     string
		groups   []groupSpec
		reserver *fakeReserver
		newNodes int
		// want maps every group id in groups to its expected final size.
		want map[string]int
	}{
		{
			name: "independent per-group quotas",
			groups: []groupSpec{
				{id: "a", min: 1, max: 100, size: 1},
				{id: "b", min: 1, max: 100, size: 1},
				{id: "c", min: 1, max: 100, size: 1},
			},
			reserver: newFakeReserver(
				pool(5, "a"),
				pool(1, "b"),
				pool(1, "c"),
			),
			newNodes: 100,
			want:     map[string]int{"a": 6, "b": 2, "c": 2},
		},
		{
			name: "fully shared pool splits evenly",
			groups: []groupSpec{
				{id: "a", min: 1, max: 100, size: 1},
				{id: "b", min: 1, max: 100, size: 1},
				{id: "c", min: 1, max: 100, size: 1},
			},
			reserver: newFakeReserver(pool(6, "a", "b", "c")),
			newNodes: 24,
			want:     map[string]int{"a": 3, "b": 3, "c": 3},
		},
		{
			name: "intersecting quotas: shared group sorts first, greedy picks it and loses both pools",
			groups: []groupSpec{
				{id: "a", min: 1, max: 100, size: 1},
				{id: "b", min: 1, max: 100, size: 1},
				{id: "c", min: 1, max: 100, size: 1},
			},
			reserver: newFakeReserver(
				pool(1, "a", "b"),
				pool(1, "a", "c"),
			),
			newNodes: 2,
			// A feasible allocation exists that places both requested nodes
			// (0, 1, 1), but the greedy fill doesn't search for it: it fills the
			// smallest-by-id group first, "a" spends both pools' single unit of
			// budget on itself, and "b"/"c" are then refused. Total placed: 1.
			// This never violates a quota and is documented as a known
			// heuristic limitation, not a bug.
			want: map[string]int{"a": 2, "b": 1, "c": 1},
		},
		{
			name: "intersecting quotas: shared group sorts last, greedy happens to find the better split",
			groups: []groupSpec{
				{id: "b", min: 1, max: 100, size: 1},
				{id: "c", min: 1, max: 100, size: 1},
				{id: "shared", min: 1, max: 100, size: 1},
			},
			reserver: newFakeReserver(
				pool(1, "shared", "b"),
				pool(1, "shared", "c"),
			),
			newNodes: 2,
			// Same quota topology as above, but "b" and "c" now sort before
			// "shared" (tie-broken by id), so they each claim their pool's unit
			// before "shared" is ever tried. Total placed: 2, the feasible
			// optimum - reached by iteration order, not by the algorithm
			// searching for it.
			want: map[string]int{"b": 2, "c": 2, "shared": 1},
		},
		{
			name: "overlapping quotas sacrifice throughput for an even split",
			groups: []groupSpec{
				{id: "a", min: 1, max: 100, size: 1},
				{id: "b", min: 1, max: 100, size: 1},
				{id: "c", min: 1, max: 100, size: 1},
			},
			reserver: newFakeReserver(
				pool(2, "a", "b"),
				pool(2, "b", "c"),
			),
			newNodes: 4,
			// Feasible allocations exist that place 4 nodes total (e.g. a+2,
			// c+2, b+0), but the greedy fill grows a, b and c together; b sits
			// in both pools, so growing it drains both at once. Total placed
			// tops out at 3 (a+1, b+1, c+1) even though 4 were requested and 4
			// are feasible - the documented throughput tradeoff.
			want: map[string]int{"a": 2, "b": 2, "c": 2},
		},
		{
			name: "quota budget above MaxSize: MaxSize is the binding constraint",
			groups: []groupSpec{
				{id: "a", min: 1, max: 3, size: 1},
			},
			reserver: newFakeReserver(pool(100, "a")),
			newNodes: 5,
			want:     map[string]int{"a": 3},
		},
		{
			name: "zero-budget group is excluded, others absorb the rest",
			groups: []groupSpec{
				{id: "a", min: 1, max: 100, size: 1},
				{id: "b", min: 1, max: 100, size: 1},
			},
			reserver: newFakeReserver(pool(0, "a")),
			newNodes: 4,
			want:     map[string]int{"a": 1, "b": 5},
		},
		{
			name: "group with no covering pool is unconstrained up to MaxSize",
			groups: []groupSpec{
				{id: "a", min: 1, max: 100, size: 1},
				{id: "b", min: 1, max: 100, size: 1},
			},
			reserver: newFakeReserver(pool(1, "a")),
			newNodes: 5,
			want:     map[string]int{"a": 2, "b": 5},
		},
		{
			name: "single group refused on the very first node: no panic, empty result",
			groups: []groupSpec{
				{id: "a", min: 1, max: 100, size: 1},
			},
			reserver: newFakeReserver(pool(0, "a")),
			newNodes: 3,
			want:     map[string]int{"a": 1},
		},
		{
			name: "single group refused partway through the fill",
			groups: []groupSpec{
				{id: "a", min: 1, max: 100, size: 1},
			},
			reserver: newFakeReserver(pool(2, "a")),
			newNodes: 5,
			want:     map[string]int{"a": 3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			groups := buildGroupsForQuotaTest(tt.groups)
			result, err := processor.BalanceScaleUpBetweenGroups(autoscalingCtx, groups, tt.newNodes, tt.reserver.ReserveNode)
			assert.NoError(t, err)

			got := make(map[string]int, len(tt.groups))
			for _, s := range tt.groups {
				got[s.id] = s.size
			}
			for _, sui := range result {
				got[sui.Group.Id()] = sui.NewSize
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestBalanceScaleUpBetweenGroupsWithQuotaNoPanicOnImmediateRefusal guards the
// crash fixed by the startIndex bound: a group refused on its very first node,
// long before MaxSize, used to leave currentIndex out of bounds.
func TestBalanceScaleUpBetweenGroupsWithQuotaNoPanicOnImmediateRefusal(t *testing.T) {
	processor := NewDefaultNodeGroupSetProcessor([]string{}, config.NodeGroupDifferenceRatios{}).(*BalancingNodeGroupSetProcessor)
	autoscalingCtx := &ca_context.AutoscalingContext{}
	groups := buildGroupsForQuotaTest([]groupSpec{{id: "a", min: 1, max: 100, size: 1}})
	reserver := newFakeReserver(pool(0, "a"))

	assert.NotPanics(t, func() {
		result, err := processor.BalanceScaleUpBetweenGroups(autoscalingCtx, groups, 1, reserver.ReserveNode)
		assert.NoError(t, err)
		assert.Empty(t, result)
	})
}
