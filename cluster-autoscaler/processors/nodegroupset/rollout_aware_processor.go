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
	gocontext "context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	"k8s.io/autoscaler/cluster-autoscaler/context"
	"k8s.io/autoscaler/cluster-autoscaler/simulator/framework"
	"k8s.io/autoscaler/cluster-autoscaler/utils/errors"
	kube_client "k8s.io/client-go/kubernetes"

	klog "k8s.io/klog/v2"
)

// RolloutState describes the current phase and target for a rollout pair.
type RolloutState struct {
	Phase       string // "", "canary", "ramping", "draining", "circuit-breaker-tripped"
	GreenTarget int    // max Green nodes at current ramp step
}

// GroupRolloutInfo ties a node group to its sibling and rollout state.
type GroupRolloutInfo struct {
	SiblingID string
	Role      string // "blue" or "green"
	State     RolloutState
}

// RolloutAwareProcessor is a NodeGroupSetProcessor that intercepts
// BalanceScaleUpBetweenGroups to distribute nodes between Blue/Green ASGs
// based on rollout state. For non-rollout groups it delegates to the
// underlying processor.
type RolloutAwareProcessor struct {
	delegate     NodeGroupSetProcessor
	rolloutIndex map[string]GroupRolloutInfo // groupID → info
}

// NewRolloutAwareProcessor creates a RolloutAwareProcessor wrapping the given delegate.
func NewRolloutAwareProcessor(delegate NodeGroupSetProcessor, rolloutIndex map[string]GroupRolloutInfo) *RolloutAwareProcessor {
	if rolloutIndex == nil {
		rolloutIndex = make(map[string]GroupRolloutInfo)
	}
	return &RolloutAwareProcessor{
		delegate:     delegate,
		rolloutIndex: rolloutIndex,
	}
}

// FindSimilarNodeGroups delegates to the underlying processor, then injects
// the rollout sibling if it's not already in the result set.
func (p *RolloutAwareProcessor) FindSimilarNodeGroups(ctx *context.AutoscalingContext, nodeGroup cloudprovider.NodeGroup,
	nodeInfosForGroups map[string]*framework.NodeInfo) ([]cloudprovider.NodeGroup, errors.AutoscalerError) {

	result, err := p.delegate.FindSimilarNodeGroups(ctx, nodeGroup, nodeInfosForGroups)
	if err != nil {
		return result, err
	}

	info, inRollout := p.rolloutIndex[nodeGroup.Id()]
	if !inRollout || info.SiblingID == "" {
		return result, nil
	}

	// Check if sibling is already present
	for _, ng := range result {
		if ng.Id() == info.SiblingID {
			return result, nil
		}
	}

	// Find sibling in cloud provider and inject it
	for _, ng := range ctx.CloudProvider.NodeGroups() {
		if ng.Id() == info.SiblingID {
			klog.V(2).Infof("RolloutAwareProcessor: injecting sibling %s for %s", info.SiblingID, nodeGroup.Id())
			result = append(result, ng)
			return result, nil
		}
	}

	klog.Warningf("RolloutAwareProcessor: sibling %s not found in cloud provider for %s", info.SiblingID, nodeGroup.Id())
	return result, nil
}

// BalanceScaleUpBetweenGroups distributes newNodes across groups. For rollout
// pairs it applies phase-aware logic; for other groups it delegates.
func (p *RolloutAwareProcessor) BalanceScaleUpBetweenGroups(ctx *context.AutoscalingContext, groups []cloudprovider.NodeGroup, newNodes int) ([]ScaleUpInfo, errors.AutoscalerError) {
	blue, green, others := p.partitionRolloutPair(groups)
	if blue == nil || green == nil {
		// No rollout pair found — pure delegation
		return p.delegate.BalanceScaleUpBetweenGroups(ctx, groups, newNodes)
	}

	blueInfo := p.rolloutIndex[blue.Id()]
	state := blueInfo.State

	blueCurrent, err := blue.TargetSize()
	if err != nil {
		return nil, errors.NewAutoscalerErrorf(errors.CloudProviderError, "failed to get blue group size: %v", err)
	}
	greenCurrent, err := green.TargetSize()
	if err != nil {
		return nil, errors.NewAutoscalerErrorf(errors.CloudProviderError, "failed to get green group size: %v", err)
	}

	var blueDelta, greenDelta int

	switch state.Phase {
	case "canary", "circuit-breaker-tripped", "":
		// All new nodes go to Blue
		blueDelta = newNodes
		greenDelta = 0
	case "draining":
		// All new nodes go to Green
		blueDelta = 0
		greenDelta = newNodes
	case "ramping":
		greenSlots := state.GreenTarget - greenCurrent
		if greenSlots < 0 {
			greenSlots = 0
		}
		// Cap by Green's MaxSize
		greenMaxDelta := green.MaxSize() - greenCurrent
		if greenMaxDelta < 0 {
			greenMaxDelta = 0
		}
		if greenSlots > greenMaxDelta {
			greenSlots = greenMaxDelta
		}
		greenDelta = greenSlots
		if greenDelta > newNodes {
			greenDelta = newNodes
		}
		blueDelta = newNodes - greenDelta
	default:
		klog.Warningf("RolloutAwareProcessor: unknown phase %q, delegating", state.Phase)
		return p.delegate.BalanceScaleUpBetweenGroups(ctx, groups, newNodes)
	}

	// Cap blue delta by MaxSize
	blueMaxDelta := blue.MaxSize() - blueCurrent
	if blueMaxDelta < 0 {
		blueMaxDelta = 0
	}
	if blueDelta > blueMaxDelta {
		blueDelta = blueMaxDelta
	}

	var result []ScaleUpInfo

	if blueDelta > 0 {
		result = append(result, ScaleUpInfo{
			Group:       blue,
			CurrentSize: blueCurrent,
			NewSize:     blueCurrent + blueDelta,
			MaxSize:     blue.MaxSize(),
		})
	}
	if greenDelta > 0 {
		result = append(result, ScaleUpInfo{
			Group:       green,
			CurrentSize: greenCurrent,
			NewSize:     greenCurrent + greenDelta,
			MaxSize:     green.MaxSize(),
		})
	}

	// If there are non-rollout groups, delegate the remainder to the standard
	// balancing processor across [blue] + others (blue absorbs whatever the
	// delegate assigns; green is handled separately).
	if len(others) > 0 && blueDelta > 0 {
		delegateGroups := append([]cloudprovider.NodeGroup{blue}, others...)
		delegateResult, err := p.delegate.BalanceScaleUpBetweenGroups(ctx, delegateGroups, blueDelta)
		if err != nil {
			return nil, err
		}
		// Replace our blue entry with the delegate's distribution
		result = filterOutGroup(result, blue.Id())
		result = append(result, delegateResult...)
		if greenDelta > 0 {
			result = append(result, ScaleUpInfo{
				Group:       green,
				CurrentSize: greenCurrent,
				NewSize:     greenCurrent + greenDelta,
				MaxSize:     green.MaxSize(),
			})
		}
	}

	return result, nil
}

// CleanUp delegates to the underlying processor.
func (p *RolloutAwareProcessor) CleanUp() {
	p.delegate.CleanUp()
}

// partitionRolloutPair splits groups into (blue, green, others).
// Returns (nil, nil, groups) if no rollout pair is found.
func (p *RolloutAwareProcessor) partitionRolloutPair(groups []cloudprovider.NodeGroup) (blue, green cloudprovider.NodeGroup, others []cloudprovider.NodeGroup) {
	for _, ng := range groups {
		info, ok := p.rolloutIndex[ng.Id()]
		if !ok {
			others = append(others, ng)
			continue
		}
		switch info.Role {
		case "blue":
			blue = ng
		case "green":
			green = ng
		default:
			others = append(others, ng)
		}
	}
	if blue == nil || green == nil {
		// Incomplete pair — treat everything as non-rollout
		allGroups := make([]cloudprovider.NodeGroup, 0, len(groups))
		allGroups = append(allGroups, groups...)
		return nil, nil, allGroups
	}
	return blue, green, others
}

func filterOutGroup(infos []ScaleUpInfo, groupID string) []ScaleUpInfo {
	result := make([]ScaleUpInfo, 0, len(infos))
	for _, info := range infos {
		if info.Group.Id() != groupID {
			result = append(result, info)
		}
	}
	return result
}

// rolloutConfigMapEntry is the JSON structure for a single rollout pair in the ConfigMap.
type rolloutConfigMapEntry struct {
	BlueID      string `json:"blueId"`
	GreenID     string `json:"greenId"`
	Phase       string `json:"phase"`
	GreenTarget int    `json:"greenTarget"`
}

// LoadRolloutIndexFromConfigMap reads a ConfigMap and builds a GroupRolloutInfo index.
// Returns (nil, nil) if the ConfigMap doesn't exist (no rollout active).
// Returns an error if the ConfigMap exists but is malformed.
func LoadRolloutIndexFromConfigMap(client kube_client.Interface, namespace, name string) (map[string]GroupRolloutInfo, error) {
	cm, err := client.CoreV1().ConfigMaps(namespace).Get(gocontext.TODO(), name, metav1.GetOptions{})
	if err != nil {
		// ConfigMap not found is not an error — just means no rollout active
		klog.V(2).Infof("RolloutAwareProcessor: no %s/%s ConfigMap found: %v", namespace, name, err)
		return nil, nil
	}

	pairsJSON, ok := cm.Data["pairs"]
	if !ok {
		return nil, fmt.Errorf("ConfigMap %s/%s missing 'pairs' key", namespace, name)
	}

	var entries []rolloutConfigMapEntry
	if err := json.Unmarshal([]byte(pairsJSON), &entries); err != nil {
		return nil, fmt.Errorf("failed to parse rollout config from %s/%s: %w", namespace, name, err)
	}

	index := make(map[string]GroupRolloutInfo, len(entries)*2)
	for _, e := range entries {
		state := RolloutState{Phase: e.Phase, GreenTarget: e.GreenTarget}
		index[e.BlueID] = GroupRolloutInfo{SiblingID: e.GreenID, Role: "blue", State: state}
		index[e.GreenID] = GroupRolloutInfo{SiblingID: e.BlueID, Role: "green", State: state}
		klog.V(2).Infof("RolloutAwareProcessor: loaded pair blue=%s green=%s phase=%s greenTarget=%d",
			e.BlueID, e.GreenID, e.Phase, e.GreenTarget)
	}
	return index, nil
}
