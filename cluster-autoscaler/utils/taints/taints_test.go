package taints

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestHasNidhoggTaint(t *testing.T) {
	nodeWithoutTaint := &apiv1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "nodeWithTaint",
		},
		Status: apiv1.NodeStatus{
			Capacity:    apiv1.ResourceList{},
			Allocatable: apiv1.ResourceList{},
		},
	}
	assert.False(t, hasNidhoggTaint(nodeWithoutTaint))

	nodeWithNidhoggTaint := &apiv1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "nodeWithTaint",
		},
		Spec: apiv1.NodeSpec{
			Taints: []apiv1.Taint{
				{
					Key:    "nidhogg.uswitch.com/kube2iam.kube2iam",
					Value:  "foo",
					Effect: "NoSchedule",
				},
			},
		},
		Status: apiv1.NodeStatus{
			Capacity:    apiv1.ResourceList{},
			Allocatable: apiv1.ResourceList{},
		},
	}
	assert.True(t, hasNidhoggTaint(nodeWithNidhoggTaint))

	nodeWithOtherTaints := &apiv1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "nodeWithTaint",
		},
		Spec: apiv1.NodeSpec{
			Taints: []apiv1.Taint{
				{
					Key:    "node",
					Value:  "foo",
					Effect: "NoSchedule",
				},
				{
					Key:    "bode",
					Value:  "foo",
					Effect: "NoSchedule",
				},
			},
		},
		Status: apiv1.NodeStatus{
			Capacity:    apiv1.ResourceList{},
			Allocatable: apiv1.ResourceList{},
		},
	}
	assert.False(t, hasNidhoggTaint(nodeWithOtherTaints))
}

func TestFilterOutNodesWithTaints(t *testing.T) {
	expectedReadiness := make(map[string]bool)

	readyCondition := apiv1.NodeCondition{
		Type:   apiv1.NodeReady,
		Status: apiv1.ConditionTrue,
	}
	notreadyCondition := apiv1.NodeCondition{
		Type:   apiv1.NodeReady,
		Status: apiv1.ConditionFalse,
	}

	nodeNoNidhoggTaintReady := &apiv1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "nodeNoNidhoggTaintReady",
		},
		Spec: apiv1.NodeSpec{
			Taints: []apiv1.Taint{
				{
					Key:    "foo",
					Value:  "boo",
					Effect: "NoSchedule",
				},
			},
		},
		Status: apiv1.NodeStatus{
			Capacity:    apiv1.ResourceList{},
			Allocatable: apiv1.ResourceList{},
			Conditions:  []apiv1.NodeCondition{readyCondition},
		},
	}
	expectedReadiness[nodeNoNidhoggTaintReady.Name] = true

	nodeNoNidhoggTaintNotReady := &apiv1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "nodeNoNidhoggTaintNotReady",
		},
		Spec: apiv1.NodeSpec{
			Taints: []apiv1.Taint{
				{
					Key:    "foo",
					Value:  "boo",
					Effect: "NoSchedule",
				},
			},
		},
		Status: apiv1.NodeStatus{
			Capacity:    apiv1.ResourceList{},
			Allocatable: apiv1.ResourceList{},
			Conditions:  []apiv1.NodeCondition{notreadyCondition},
		},
	}
	expectedReadiness[nodeNoNidhoggTaintNotReady.Name] = false

	nodeHasNidhoggTaintReady := &apiv1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "nodeHasNidhoggTaintReady",
		},
		Spec: apiv1.NodeSpec{
			Taints: []apiv1.Taint{
				{
					Key:    "nidhogg.uswitch.com/foo.bar",
					Value:  "baz",
					Effect: "NoSchedule",
				},
			},
		},
		Status: apiv1.NodeStatus{
			Capacity:    apiv1.ResourceList{},
			Allocatable: apiv1.ResourceList{},
			Conditions:  []apiv1.NodeCondition{readyCondition},
		},
	}
	expectedReadiness[nodeHasNidhoggTaintReady.Name] = false

	nodeHasNidhoggTaintNotReady := &apiv1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "nodeHasNidhoggTaintNotReady",
		},
		Spec: apiv1.NodeSpec{
			Taints: []apiv1.Taint{
				{
					Key:    "nidhogg.uswitch.com/x.y",
					Value:  "z",
					Effect: "NoSchedule",
				},
			},
		},
		Status: apiv1.NodeStatus{
			Capacity:    apiv1.ResourceList{},
			Allocatable: apiv1.ResourceList{},
			Conditions:  []apiv1.NodeCondition{notreadyCondition},
		},
	}
	expectedReadiness[nodeHasNidhoggTaintNotReady.Name] = false

	initialReadyNodes := []*apiv1.Node{
		nodeNoNidhoggTaintReady,
		nodeHasNidhoggTaintReady,
	}
	initialAllNodes := []*apiv1.Node{
		nodeNoNidhoggTaintReady,
		nodeNoNidhoggTaintNotReady,
		nodeHasNidhoggTaintReady,
		nodeHasNidhoggTaintNotReady,
	}

	newAllNodes, newReadyNodes := FilterOutNodesWithTaints(initialAllNodes, initialReadyNodes)

	foundInReady := make(map[string]bool)
	for _, node := range newReadyNodes {
		foundInReady[node.Name] = true
		assert.True(t, expectedReadiness[node.Name], fmt.Sprintf("Node %s found in ready nodes list (it shouldn't be there)", node.Name))
	}
	for nodeName, expected := range expectedReadiness {
		if expected {
			assert.True(t, foundInReady[nodeName], fmt.Sprintf("Node %s expected ready, but not found in ready nodes list", nodeName))
		}
	}
	for _, node := range newAllNodes {
		assert.Equal(t, len(node.Status.Conditions), 1)
		if expectedReadiness[node.Name] {
			assert.Equal(t, node.Status.Conditions[0].Status, apiv1.ConditionTrue, fmt.Sprintf("Unexpected ready condition value for node %s", node.Name))
		} else {
			assert.Equal(t, node.Status.Conditions[0].Status, apiv1.ConditionFalse, fmt.Sprintf("Unexpected ready condition value for node %s", node.Name))
		}
	}
}
