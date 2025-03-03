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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/autoscaler/cluster-autoscaler/context"
	"k8s.io/autoscaler/cluster-autoscaler/processors/datadog/common"
	"k8s.io/autoscaler/cluster-autoscaler/simulator/clustersnapshot/testsnapshot"
	"k8s.io/autoscaler/cluster-autoscaler/simulator/framework"
)

func TestTransformDataNodesProcess(t *testing.T) {
	localStorageValue := "20G"
	localStorageQuantity := resource.MustParse(localStorageValue)
	tests := []struct {
		name     string
		node     *corev1.Node
		expected *corev1.Node
	}{

		{
			"Resource is added to fresh nodes having local-data label",
			buildTestNode("a", NodeReadyGraceDelay/2, true, localStorageValue, nil),
			buildTestNode("a", NodeReadyGraceDelay/2, true, localStorageValue, &localStorageQuantity),
		},

		{
			"Resource is not added to old nodes having local-data label",
			buildTestNode("b", 2*NodeReadyGraceDelay, true, localStorageValue, nil),
			buildTestNode("b", 2*NodeReadyGraceDelay, true, localStorageValue, nil),
		},

		{
			"Resource is not added to new nodes without local-data label",
			buildTestNode("c", NodeReadyGraceDelay/2, false, "", nil),
			buildTestNode("c", NodeReadyGraceDelay/2, false, "", nil),
		},

		{
			"Resource is not added to new nodes without local-data label but has local storage capacity label",
			buildTestNode("d", NodeReadyGraceDelay/2, false, localStorageValue, nil),
			buildTestNode("d", NodeReadyGraceDelay/2, false, localStorageValue, nil),
		},

		{
			"Default resource is added to new nodes without local storage capacity label",
			buildTestNode("e", NodeReadyGraceDelay/2, true, "", nil),
			buildTestNode("e", NodeReadyGraceDelay/2, true, "", common.DatadogLocalDataQuantity),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clusterSnapshot := testsnapshot.NewTestSnapshotOrDie(t)
			err := clusterSnapshot.AddNodeInfo(framework.NewTestNodeInfo(tt.node))
			assert.NoError(t, err)

			ctx := &context.AutoscalingContext{
				ClusterSnapshot: clusterSnapshot,
			}

			proc := NewTransformDataNodes()
			_, err = proc.Process(ctx, []*corev1.Pod{})
			assert.NoError(t, err)

			actual, err := ctx.ClusterSnapshot.NodeInfos().Get(tt.node.GetName())
			assert.NoError(t, err)

			assert.Equal(t, tt.expected.Status.Capacity, actual.Node().Status.Capacity)
			assert.Equal(t, tt.expected.Status.Allocatable, actual.Node().Status.Allocatable)
		})
	}

}

func buildTestNode(name string, age time.Duration, localDataLabel bool, localStorageCapacityLabel string, localDataQuantity *resource.Quantity) *corev1.Node {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			SelfLink:          fmt.Sprintf("/api/v1/nodes/%s", name),
			Labels:            map[string]string{},
			CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
		},
		Status: corev1.NodeStatus{
			Capacity:    corev1.ResourceList{},
			Allocatable: corev1.ResourceList{},
			Conditions: []corev1.NodeCondition{
				{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(time.Now().Add(-age)),
				},
			},
		},
	}

	if localDataLabel {
		node.ObjectMeta.Labels[common.DatadogLocalStorageLabel] = "true"
	}
	if len(localStorageCapacityLabel) > 0 {
		node.ObjectMeta.Labels[common.DatadogLocalStorageCapacityLabel] = localStorageCapacityLabel
	}

	if localDataQuantity != nil {
		node.Status.Capacity[common.DatadogLocalDataExistsResource] = common.DatadogLocalDataQuantity.DeepCopy()
		node.Status.Allocatable[common.DatadogLocalDataExistsResource] = common.DatadogLocalDataQuantity.DeepCopy()

		node.Status.Capacity[common.DatadogLocalStorageResource] = localDataQuantity.DeepCopy()
		node.Status.Allocatable[common.DatadogLocalStorageResource] = localDataQuantity.DeepCopy()
	}

	return node
}
