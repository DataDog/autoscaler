/*
Copyright 2026 The Kubernetes Authors.

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
	"k8s.io/autoscaler/cluster-autoscaler/context"
	"k8s.io/autoscaler/cluster-autoscaler/simulator/framework"
	. "k8s.io/autoscaler/cluster-autoscaler/utils/test"

	"github.com/stretchr/testify/assert"
)

// helpers

func buildRolloutProvider(groups map[string][3]int) *testprovider.TestCloudProvider {
	provider := testprovider.NewTestCloudProviderBuilder().Build()
	for id, spec := range groups {
		// spec: [min, max, size]
		provider.AddNodeGroup(id, spec[0], spec[1], spec[2])
		n := BuildTestNode("n-"+id, 1000, 1000)
		provider.AddNode(id, n)
	}
	return provider
}

func rolloutGroups(provider *testprovider.TestCloudProvider, ids ...string) []cloudprovider.NodeGroup {
	groupMap := make(map[string]cloudprovider.NodeGroup)
	for _, ng := range provider.NodeGroups() {
		groupMap[ng.Id()] = ng
	}
	result := make([]cloudprovider.NodeGroup, 0, len(ids))
	for _, id := range ids {
		result = append(result, groupMap[id])
	}
	return result
}

func toMap(infos []ScaleUpInfo) map[string]ScaleUpInfo {
	m := make(map[string]ScaleUpInfo, len(infos))
	for _, info := range infos {
		m[info.Group.Id()] = info
	}
	return m
}

func rolloutPairIndex(blueID, greenID string, state RolloutState) map[string]GroupRolloutInfo {
	return map[string]GroupRolloutInfo{
		blueID: {
			SiblingID: greenID,
			Role:      "blue",
			State:     state,
		},
		greenID: {
			SiblingID: blueID,
			Role:      "green",
			State:     state,
		},
	}
}

// alwaysSimilar is a comparator that considers all nodes similar.
func alwaysSimilar(n1, n2 *framework.NodeInfo) bool { return true }

// neverSimilar is a comparator that considers no nodes similar.
func neverSimilar(n1, n2 *framework.NodeInfo) bool { return false }

// Step 1: No rollout — pure delegation

func TestRolloutAware_NoRollout_Delegation(t *testing.T) {
	provider := buildRolloutProvider(map[string][3]int{
		"ng1": {1, 10, 1},
		"ng2": {1, 10, 3},
	})
	ctx := &context.AutoscalingContext{CloudProvider: provider}

	delegate := &BalancingNodeGroupSetProcessor{Comparator: alwaysSimilar}
	processor := NewRolloutAwareProcessor(delegate, nil)

	groups := rolloutGroups(provider, "ng1", "ng2")
	scaleUp, err := processor.BalanceScaleUpBetweenGroups(ctx, groups, 4)
	assert.NoError(t, err)

	// Standard balancing: ng1=1, ng2=3, +4 → ng1=3, ng2=3 (ng1 gets +2 to catch up, then both get 1 more → ng1=3, ng2=4? Let's check)
	// Actually: sorted [ng1(1), ng2(3)], add to smallest: 1→2→3→3, then both at 3, add one more → one gets 4.
	// With 4 nodes: 1→2, 1→3, both=3→one gets 4, one gets 4 → ng1=3, ng2=5? No...
	// ng1=1+2=3, ng2=3+0=3 (2 used), then 2 remaining split: ng1=4, ng2=4. Total: ng1 gets +3, ng2 gets +1
	m := toMap(scaleUp)
	// Both should end up balanced. ng1 starts at 1, ng2 at 3, +4 total = 8 nodes, 8/2=4 each
	assert.Equal(t, 2, len(scaleUp))
	assert.Equal(t, 4, m["ng1"].NewSize)
	assert.Equal(t, 4, m["ng2"].NewSize)
}

// Step 2: Canary phase — all to Blue

func TestRolloutAware_Canary_AllToBlue(t *testing.T) {
	provider := buildRolloutProvider(map[string][3]int{
		"blue":  {1, 100, 10},
		"green": {0, 100, 0},
	})
	ctx := &context.AutoscalingContext{CloudProvider: provider}

	index := rolloutPairIndex("blue", "green", RolloutState{Phase: "canary"})
	delegate := &BalancingNodeGroupSetProcessor{Comparator: alwaysSimilar}
	processor := NewRolloutAwareProcessor(delegate, index)

	groups := rolloutGroups(provider, "blue", "green")
	scaleUp, err := processor.BalanceScaleUpBetweenGroups(ctx, groups, 10)
	assert.NoError(t, err)

	m := toMap(scaleUp)
	assert.Equal(t, 1, len(scaleUp))
	assert.Equal(t, 20, m["blue"].NewSize)
	_, greenPresent := m["green"]
	assert.False(t, greenPresent)
}

// Step 3: Circuit breaker — all to Blue

func TestRolloutAware_CircuitBreaker_AllToBlue(t *testing.T) {
	provider := buildRolloutProvider(map[string][3]int{
		"blue":  {1, 100, 10},
		"green": {0, 100, 5},
	})
	ctx := &context.AutoscalingContext{CloudProvider: provider}

	index := rolloutPairIndex("blue", "green", RolloutState{Phase: "circuit-breaker-tripped"})
	delegate := &BalancingNodeGroupSetProcessor{Comparator: alwaysSimilar}
	processor := NewRolloutAwareProcessor(delegate, index)

	groups := rolloutGroups(provider, "blue", "green")
	scaleUp, err := processor.BalanceScaleUpBetweenGroups(ctx, groups, 10)
	assert.NoError(t, err)

	m := toMap(scaleUp)
	assert.Equal(t, 1, len(scaleUp))
	assert.Equal(t, 20, m["blue"].NewSize)
}

// Step 4: Draining — all to Green

func TestRolloutAware_Draining_AllToGreen(t *testing.T) {
	provider := buildRolloutProvider(map[string][3]int{
		"blue":  {1, 100, 10},
		"green": {0, 100, 50},
	})
	ctx := &context.AutoscalingContext{CloudProvider: provider}

	index := rolloutPairIndex("blue", "green", RolloutState{Phase: "draining"})
	delegate := &BalancingNodeGroupSetProcessor{Comparator: alwaysSimilar}
	processor := NewRolloutAwareProcessor(delegate, index)

	groups := rolloutGroups(provider, "blue", "green")
	scaleUp, err := processor.BalanceScaleUpBetweenGroups(ctx, groups, 10)
	assert.NoError(t, err)

	m := toMap(scaleUp)
	assert.Equal(t, 1, len(scaleUp))
	assert.Equal(t, 60, m["green"].NewSize)
	_, bluePresent := m["blue"]
	assert.False(t, bluePresent)
}

// Step 5: Ramping — split by greenTarget

func TestRolloutAware_Ramping_SplitByGreenTarget(t *testing.T) {
	provider := buildRolloutProvider(map[string][3]int{
		"blue":  {1, 100, 50},
		"green": {0, 100, 2},
	})
	ctx := &context.AutoscalingContext{CloudProvider: provider}

	index := rolloutPairIndex("blue", "green", RolloutState{Phase: "ramping", GreenTarget: 8})
	delegate := &BalancingNodeGroupSetProcessor{Comparator: alwaysSimilar}
	processor := NewRolloutAwareProcessor(delegate, index)

	groups := rolloutGroups(provider, "blue", "green")
	scaleUp, err := processor.BalanceScaleUpBetweenGroups(ctx, groups, 20)
	assert.NoError(t, err)

	m := toMap(scaleUp)
	assert.Equal(t, 2, len(scaleUp))
	// Green: 2 → 8 (+6), Blue: 50 + 14 = 64
	assert.Equal(t, 8, m["green"].NewSize)
	assert.Equal(t, 64, m["blue"].NewSize)
}

// Step 6: Ramping — greenTarget already met

func TestRolloutAware_Ramping_GreenTargetMet(t *testing.T) {
	provider := buildRolloutProvider(map[string][3]int{
		"blue":  {1, 100, 50},
		"green": {0, 100, 8},
	})
	ctx := &context.AutoscalingContext{CloudProvider: provider}

	index := rolloutPairIndex("blue", "green", RolloutState{Phase: "ramping", GreenTarget: 8})
	delegate := &BalancingNodeGroupSetProcessor{Comparator: alwaysSimilar}
	processor := NewRolloutAwareProcessor(delegate, index)

	groups := rolloutGroups(provider, "blue", "green")
	scaleUp, err := processor.BalanceScaleUpBetweenGroups(ctx, groups, 10)
	assert.NoError(t, err)

	m := toMap(scaleUp)
	assert.Equal(t, 1, len(scaleUp))
	assert.Equal(t, 60, m["blue"].NewSize)
}

// Step 7: Ramping — request fits entirely in Green

func TestRolloutAware_Ramping_AllToGreen(t *testing.T) {
	provider := buildRolloutProvider(map[string][3]int{
		"blue":  {1, 100, 50},
		"green": {0, 100, 5},
	})
	ctx := &context.AutoscalingContext{CloudProvider: provider}

	index := rolloutPairIndex("blue", "green", RolloutState{Phase: "ramping", GreenTarget: 20})
	delegate := &BalancingNodeGroupSetProcessor{Comparator: alwaysSimilar}
	processor := NewRolloutAwareProcessor(delegate, index)

	groups := rolloutGroups(provider, "blue", "green")
	scaleUp, err := processor.BalanceScaleUpBetweenGroups(ctx, groups, 3)
	assert.NoError(t, err)

	m := toMap(scaleUp)
	assert.Equal(t, 1, len(scaleUp))
	assert.Equal(t, 8, m["green"].NewSize)
	_, bluePresent := m["blue"]
	assert.False(t, bluePresent)
}

// Step 8: Ramping — respect MaxSize

func TestRolloutAware_Ramping_RespectMaxSize(t *testing.T) {
	provider := buildRolloutProvider(map[string][3]int{
		"blue":  {1, 100, 50},
		"green": {0, 10, 5}, // maxSize=10
	})
	ctx := &context.AutoscalingContext{CloudProvider: provider}

	index := rolloutPairIndex("blue", "green", RolloutState{Phase: "ramping", GreenTarget: 100})
	delegate := &BalancingNodeGroupSetProcessor{Comparator: alwaysSimilar}
	processor := NewRolloutAwareProcessor(delegate, index)

	groups := rolloutGroups(provider, "blue", "green")
	scaleUp, err := processor.BalanceScaleUpBetweenGroups(ctx, groups, 20)
	assert.NoError(t, err)

	m := toMap(scaleUp)
	assert.Equal(t, 2, len(scaleUp))
	// Green: 5 → 10 (+5, capped at maxSize), Blue: 50 + 15 = 65
	assert.Equal(t, 10, m["green"].NewSize)
	assert.Equal(t, 65, m["blue"].NewSize)
}

// Step 9: Mixed groups — rollout pair + non-rollout similar groups

func TestRolloutAware_MixedGroups(t *testing.T) {
	provider := buildRolloutProvider(map[string][3]int{
		"blue":  {1, 100, 50},
		"green": {0, 100, 2},
		"ng3":   {1, 100, 50},
	})
	ctx := &context.AutoscalingContext{CloudProvider: provider}

	index := rolloutPairIndex("blue", "green", RolloutState{Phase: "ramping", GreenTarget: 8})
	delegate := &BalancingNodeGroupSetProcessor{Comparator: alwaysSimilar}
	processor := NewRolloutAwareProcessor(delegate, index)

	groups := rolloutGroups(provider, "blue", "green", "ng3")
	scaleUp, err := processor.BalanceScaleUpBetweenGroups(ctx, groups, 20)
	assert.NoError(t, err)

	m := toMap(scaleUp)
	// Green gets 6 (2→8), remainder=14 split between blue(50) and ng3(50) via delegate
	assert.Equal(t, 8, m["green"].NewSize)
	// blue and ng3 both start at 50, delegate balances 14 across them: 7 each
	assert.Equal(t, 57, m["blue"].NewSize)
	assert.Equal(t, 57, m["ng3"].NewSize)
}

// Step 10: FindSimilarNodeGroups — sibling injection

func TestRolloutAware_FindSimilarNodeGroups_InjectsSibling(t *testing.T) {
	provider := testprovider.NewTestCloudProviderBuilder().Build()
	provider.AddNodeGroup("blue", 1, 100, 10)
	provider.AddNodeGroup("green", 0, 100, 5)

	nBlue := BuildTestNode("n-blue", 1000, 1000)
	nGreen := BuildTestNode("n-green", 2000, 2000) // different capacity — delegate won't match
	provider.AddNode("blue", nBlue)
	provider.AddNode("green", nGreen)

	niBlue := framework.NewTestNodeInfo(nBlue)
	niGreen := framework.NewTestNodeInfo(nGreen)

	ctx := &context.AutoscalingContext{CloudProvider: provider}
	nodeInfos := map[string]*framework.NodeInfo{
		"blue":  niBlue,
		"green": niGreen,
	}

	// neverSimilar delegate won't match anything
	delegate := &BalancingNodeGroupSetProcessor{Comparator: neverSimilar}
	index := rolloutPairIndex("blue", "green", RolloutState{Phase: "ramping", GreenTarget: 10})
	processor := NewRolloutAwareProcessor(delegate, index)

	green, _ := ctx.CloudProvider.NodeGroupForNode(nGreen)
	similar, err := processor.FindSimilarNodeGroups(ctx, green, nodeInfos)
	assert.NoError(t, err)

	// Blue should be injected even though delegate returned empty
	assert.Equal(t, 1, len(similar))
	assert.Equal(t, "blue", similar[0].Id())
}

// Step 11: FindSimilarNodeGroups — no injection when not in rollout

func TestRolloutAware_FindSimilarNodeGroups_NoInjectionWithoutRollout(t *testing.T) {
	provider := testprovider.NewTestCloudProviderBuilder().Build()
	provider.AddNodeGroup("ng1", 1, 100, 10)
	provider.AddNodeGroup("ng2", 0, 100, 5)

	n1 := BuildTestNode("n1", 1000, 1000)
	n2 := BuildTestNode("n2", 2000, 2000)
	provider.AddNode("ng1", n1)
	provider.AddNode("ng2", n2)

	ni1 := framework.NewTestNodeInfo(n1)
	ni2 := framework.NewTestNodeInfo(n2)

	ctx := &context.AutoscalingContext{CloudProvider: provider}
	nodeInfos := map[string]*framework.NodeInfo{
		"ng1": ni1,
		"ng2": ni2,
	}

	delegate := &BalancingNodeGroupSetProcessor{Comparator: neverSimilar}
	processor := NewRolloutAwareProcessor(delegate, nil) // no rollout index

	ng1, _ := ctx.CloudProvider.NodeGroupForNode(n1)
	similar, err := processor.FindSimilarNodeGroups(ctx, ng1, nodeInfos)
	assert.NoError(t, err)
	assert.Equal(t, 0, len(similar))
}

// Step: Empty phase treated same as canary (all to Blue)

func TestRolloutAware_EmptyPhase_AllToBlue(t *testing.T) {
	provider := buildRolloutProvider(map[string][3]int{
		"blue":  {1, 100, 10},
		"green": {0, 100, 0},
	})
	ctx := &context.AutoscalingContext{CloudProvider: provider}

	index := rolloutPairIndex("blue", "green", RolloutState{Phase: ""})
	delegate := &BalancingNodeGroupSetProcessor{Comparator: alwaysSimilar}
	processor := NewRolloutAwareProcessor(delegate, index)

	groups := rolloutGroups(provider, "blue", "green")
	scaleUp, err := processor.BalanceScaleUpBetweenGroups(ctx, groups, 5)
	assert.NoError(t, err)

	m := toMap(scaleUp)
	assert.Equal(t, 1, len(scaleUp))
	assert.Equal(t, 15, m["blue"].NewSize)
}
