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
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	schedulerframework "k8s.io/kubernetes/pkg/scheduler/framework"
)

func TestNodeHasLocalData(t *testing.T) {
	tests := []struct {
		name     string
		node     *corev1.Node
		expected bool
	}{
		{
			"no labels at all means no local storage",
			&corev1.Node{},
			false,
		},
		{
			"no local-data label means no local storage",
			&corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"spam": "egg"},
				},
			},
			false,
		},
		{
			"local-data:false label means no local storage",
			&corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{DatadogLocalStorageLabel: "false"},
				},
			},
			false,
		},
		{
			"local-data:true label means local storage",
			&corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{DatadogLocalStorageLabel: "true"},
				},
			},
			true,
		},
		{
			"nil node doesn't crash, means no local storage",
			nil,
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, NodeHasLocalData(tt.node), tt.expected)
		})
	}
}

func TestSetNodeLocalDataResourceDefault(t *testing.T) {
	ni := schedulerframework.NewNodeInfo(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "spam"},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "egg"},
		},
	)
	ni.SetNode(&corev1.Node{})

	SetNodeLocalDataResource(ni)

	nodeValue, ok := ni.Node().Status.Allocatable[DatadogLocalDataExistsResource]
	assert.True(t, ok)
	assert.Equal(t, nodeValue, *resource.NewQuantity(1, resource.DecimalSI))

	niValue, ok := ni.Allocatable.ScalarResources[DatadogLocalDataExistsResource]
	assert.True(t, ok)
	assert.Equal(t, niValue, int64(1))

	nodeValue, ok = ni.Node().Status.Allocatable[DatadogLocalStorageResource]
	assert.True(t, ok)
	assert.Equal(t, nodeValue, *resource.NewQuantity(1, resource.DecimalSI))

	niValue, ok = ni.Allocatable.ScalarResources[DatadogLocalStorageResource]
	assert.True(t, ok)
	assert.Equal(t, niValue, int64(1))

	assert.Equal(t, len(ni.Pods), 2)
}

func TestSetNodeLocalDataResourceWithLocalStorageCapacity(t *testing.T) {
	localStorage := "100Gi"
	localStorageQuantity := resource.MustParse(localStorage)
	ni := schedulerframework.NewNodeInfo(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "spam"},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "egg"},
		},
	)
	ni.SetNode(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				DatadogLocalStorageCapacityLabel: localStorage,
			},
		},
	})

	SetNodeLocalDataResource(ni)

	nodeValue, ok := ni.Node().Status.Allocatable[DatadogLocalDataExistsResource]
	assert.True(t, ok)
	assert.Equal(t, nodeValue, *resource.NewQuantity(1, resource.DecimalSI))

	niValue, ok := ni.Allocatable.ScalarResources[DatadogLocalDataExistsResource]
	assert.True(t, ok)
	assert.Equal(t, niValue, int64(1))

	nodeValue, ok = ni.Node().Status.Allocatable[DatadogLocalStorageResource]
	assert.True(t, ok)
	assert.Equal(t, nodeValue, localStorageQuantity)

	niValue, ok = ni.Allocatable.ScalarResources[DatadogLocalStorageResource]
	assert.True(t, ok)
	var hundredGB int64 = 100 * 1024 * 1024 * 1024
	assert.Equal(t, niValue, hundredGB)

	assert.Equal(t, len(ni.Pods), 2)
}

func TestSetNodeLocalDataResourceWithFaultyLocalStorageCapacity(t *testing.T) {
	ni := schedulerframework.NewNodeInfo(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "spam"},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "egg"},
		},
	)
	ni.SetNode(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				DatadogLocalStorageCapacityLabel: "foo",
			},
		},
	})

	SetNodeLocalDataResource(ni)

	nodeValue, ok := ni.Node().Status.Allocatable[DatadogLocalDataExistsResource]
	assert.True(t, ok)
	assert.Equal(t, nodeValue, *resource.NewQuantity(1, resource.DecimalSI))

	niValue, ok := ni.Allocatable.ScalarResources[DatadogLocalDataExistsResource]
	assert.True(t, ok)
	assert.Equal(t, niValue, int64(1))

	nodeValue, ok = ni.Node().Status.Allocatable[DatadogLocalStorageResource]
	assert.True(t, ok)
	assert.Equal(t, nodeValue, *resource.NewQuantity(1, resource.DecimalSI))

	niValue, ok = ni.Allocatable.ScalarResources[DatadogLocalStorageResource]
	assert.True(t, ok)
	assert.Equal(t, niValue, int64(1))

	assert.Equal(t, len(ni.Pods), 2)
}
