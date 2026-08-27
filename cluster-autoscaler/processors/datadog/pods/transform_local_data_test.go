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
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/autoscaler/cluster-autoscaler/context"
	"k8s.io/autoscaler/cluster-autoscaler/processors/datadog/common"
	"k8s.io/client-go/kubernetes/fake"
	v1lister "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

var (
	testRemoteClass        = "remote-data"
	testLocalClass         = "local-data"
	testLocalBlockClass    = "local-data-block"
	testTopoLVMClass       = "ephemeral-local-data"
	testNamespace          = "foons"
	testEmptyResources     = corev1.ResourceList{}
	testDefaultLdResources = corev1.ResourceList{
		common.DatadogLocalDataExistsResource: common.DatadogLocalDataQuantity.DeepCopy(),
		common.DatadogLocalStorageResource:    common.DatadogLocalDataQuantity.DeepCopy(),
	}
	localStorage         = "100Gi"
	localStorageCapacity = resource.MustParse(localStorage)
	testLdResources      = corev1.ResourceList{
		common.DatadogLocalDataExistsResource: common.DatadogLocalDataQuantity.DeepCopy(),
		common.DatadogLocalStorageResource:    localStorageCapacity.DeepCopy(),
	}
	testTopoLVMResources = corev1.ResourceList{
		common.DatadogEphemeralLocalDataResource: localStorageCapacity.DeepCopy(),
	}
	testMixedResources = corev1.ResourceList{
		common.DatadogLocalDataExistsResource:    common.DatadogLocalDataQuantity.DeepCopy(),
		common.DatadogLocalStorageResource:       localStorageCapacity.DeepCopy(),
		common.DatadogEphemeralLocalDataResource: localStorageCapacity.DeepCopy(),
	}
)

func TestTransformLocalDataProcess(t *testing.T) {
	tests := []struct {
		name     string
		pods     []*corev1.Pod
		pvcs     []*corev1.PersistentVolumeClaim
		expected []*corev1.Pod
	}{
		{
			"No modification on remote volumes",
			[]*corev1.Pod{buildPod("pod1", testEmptyResources, testEmptyResources, "pvc-1")},
			[]*corev1.PersistentVolumeClaim{buildPVC("pvc-1", testRemoteClass)},
			[]*corev1.Pod{buildPod("pod1", testEmptyResources, testEmptyResources, "pvc-1")},
		},

		{
			"Cope with pod not having volumes",
			[]*corev1.Pod{buildPod("pod1", testEmptyResources, testEmptyResources)},
			[]*corev1.PersistentVolumeClaim{},
			[]*corev1.Pod{buildPod("pod1", testEmptyResources, testEmptyResources)},
		},

		{
			"local-data volumes are removed, and custom resources added with default storage",
			[]*corev1.Pod{buildPod("pod1", testEmptyResources, testEmptyResources, "pvc-1")},
			[]*corev1.PersistentVolumeClaim{buildPVC("pvc-1", testLocalClass)},
			[]*corev1.Pod{buildPod("pod1", testDefaultLdResources, testDefaultLdResources)},
		},

		{
			"local-data volumes are removed, and custom resources added with local storage capacity",
			[]*corev1.Pod{buildPod("pod1", testEmptyResources, testEmptyResources, "pvc-1")},
			[]*corev1.PersistentVolumeClaim{buildPVCWithStorage("pvc-1", testLocalClass, localStorage)},
			[]*corev1.Pod{buildPod("pod1", testLdResources, testLdResources)},
		},

		{
			"mixed local-data and remote volumes don't cause confusion",
			[]*corev1.Pod{buildPod("pod1", testEmptyResources, testEmptyResources, "pvc-1", "pvc-2", "pvc-3")},
			[]*corev1.PersistentVolumeClaim{
				buildPVC("pvc-1", testRemoteClass),
				buildPVC("pvc-2", testLocalClass),
				buildPVC("pvc-3", testRemoteClass),
			},
			[]*corev1.Pod{buildPod("pod1", testDefaultLdResources, testDefaultLdResources, "pvc-1", "pvc-3")},
		},

		{
			"local-data-block volumes are removed, and custom resources added with default storage",
			[]*corev1.Pod{buildPod("pod1", testEmptyResources, testEmptyResources, "pvc-1")},
			[]*corev1.PersistentVolumeClaim{buildPVC("pvc-1", testLocalBlockClass)},
			[]*corev1.Pod{buildPod("pod1", testDefaultLdResources, testDefaultLdResources)},
		},

		{
			"local-data-block volumes are removed, and custom resources added with local storage capacity",
			[]*corev1.Pod{buildPod("pod1", testEmptyResources, testEmptyResources, "pvc-1")},
			[]*corev1.PersistentVolumeClaim{buildPVCWithStorage("pvc-1", testLocalBlockClass, localStorage)},
			[]*corev1.Pod{buildPod("pod1", testLdResources, testLdResources)},
		},

		{
			"mixed local-data, local-data-block and remote volumes don't cause confusion",
			[]*corev1.Pod{buildPod("pod1", testEmptyResources, testEmptyResources, "pvc-1", "pvc-2", "pvc-3", "pvc-4")},
			[]*corev1.PersistentVolumeClaim{
				buildPVC("pvc-1", testRemoteClass),
				buildPVC("pvc-2", testLocalClass),
				buildPVC("pvc-3", testRemoteClass),
				buildPVC("pvc-4", testLocalBlockClass),
			},
			[]*corev1.Pod{buildPod("pod1", testDefaultLdResources, testDefaultLdResources, "pvc-1", "pvc-3")},
		},

		{
			"volumes using missing pvcs are conserved",
			[]*corev1.Pod{buildPod("pod1", testEmptyResources, testEmptyResources, "pvc-1")},
			[]*corev1.PersistentVolumeClaim{},
			[]*corev1.Pod{buildPod("pod1", testEmptyResources, testEmptyResources, "pvc-1")},
		},

		{
			"empty pod list don't crash",
			[]*corev1.Pod{},
			[]*corev1.PersistentVolumeClaim{},
			[]*corev1.Pod{},
		},
		{
			"topolvm generic ephemeral volume is replaced by its storage request",
			[]*corev1.Pod{addEphemeralVolume(buildPod("pod1", testEmptyResources, testEmptyResources, "pvc-1"), "scratch", testTopoLVMClass, localStorage)},
			[]*corev1.PersistentVolumeClaim{buildPVC("pvc-1", testRemoteClass)},
			[]*corev1.Pod{buildPod("pod1", testTopoLVMResources, testTopoLVMResources, "pvc-1")},
		},
		{
			"multiple topolvm generic ephemeral volumes are summed",
			[]*corev1.Pod{
				addEphemeralVolume(
					addEphemeralVolume(buildPod("pod1", testEmptyResources, testEmptyResources), "scratch-1", testTopoLVMClass, "100Gi"),
					"scratch-2", testTopoLVMClass, "50Gi",
				),
			},
			[]*corev1.PersistentVolumeClaim{},
			[]*corev1.Pod{buildPod("pod1", resourceList(common.DatadogEphemeralLocalDataResource, "150Gi"), resourceList(common.DatadogEphemeralLocalDataResource, "150Gi"))},
		},
		{
			"topolvm ephemeral and local-data persistent volumes remain distinct",
			[]*corev1.Pod{addEphemeralVolume(buildPod("pod1", testEmptyResources, testEmptyResources, "pvc-1"), "scratch", testTopoLVMClass, localStorage)},
			[]*corev1.PersistentVolumeClaim{buildPVCWithStorage("pvc-1", testLocalClass, localStorage)},
			[]*corev1.Pod{buildPod("pod1", testMixedResources, testMixedResources)},
		},
		{
			"persistent topolvm volume is not transformed",
			[]*corev1.Pod{buildPod("pod1", testEmptyResources, testEmptyResources, "pvc-1")},
			[]*corev1.PersistentVolumeClaim{buildPVCWithStorage("pvc-1", testTopoLVMClass, localStorage)},
			[]*corev1.Pod{buildPod("pod1", testEmptyResources, testEmptyResources, "pvc-1")},
		},
		{
			"ephemeral topolvm volume without a storage request is not transformed",
			[]*corev1.Pod{addEphemeralVolume(buildPod("pod1", testEmptyResources, testEmptyResources), "scratch", testTopoLVMClass, "")},
			[]*corev1.PersistentVolumeClaim{},
			[]*corev1.Pod{addEphemeralVolume(buildPod("pod1", testEmptyResources, testEmptyResources), "scratch", testTopoLVMClass, "")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pvcLister, err := newTestPVCLister(tt.pvcs)
			assert.NoError(t, err)

			tld := transformLocalData{
				pvcLister: pvcLister,
			}
			actual, err := tld.Process(&context.AutoscalingContext{}, tt.pods)
			assert.NoError(t, err)
			assert.True(t, apiequality.Semantic.DeepEqual(tt.expected, actual))
		})
	}

}

// TestNewPersistentVolumeClaimLister exercises the real constructor end-to-end
// (unlike TestTransformLocalDataProcess, which builds its lister directly from
// a cache.Indexer and never calls it). A prior version called factory.Start()
// before Lister() registered the informer with the factory, so the informer
// was never actually run and Get() always returned NotFound.
func TestNewPersistentVolumeClaimLister(t *testing.T) {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-pvc",
			Namespace: testNamespace,
		},
	}
	client := fake.NewClientset(pvc)
	stopCh := make(chan struct{})
	defer close(stopCh)

	lister := NewPersistentVolumeClaimLister(client, stopCh)

	assert.Eventually(t, func() bool {
		_, err := lister.PersistentVolumeClaims(testNamespace).Get("my-pvc")
		return err == nil
	}, time.Second, time.Millisecond, "lister never observed the pre-existing PVC")
}

func newTestPVCLister(pvcs []*corev1.PersistentVolumeClaim) (v1lister.PersistentVolumeClaimLister, error) {
	store := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	for _, pvc := range pvcs {
		err := store.Add(pvc)
		if err != nil {
			return nil, fmt.Errorf("Error adding object to cache: %v", err)
		}
	}
	return v1lister.NewPersistentVolumeClaimLister(store), nil
}

func buildPod(name string, requests, limits corev1.ResourceList, claimNames ...string) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
		},
		Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{},
			Containers: []corev1.Container{
				{
					Resources: corev1.ResourceRequirements{
						Requests: requests.DeepCopy(),
						Limits:   limits.DeepCopy(),
					},
				},
			},
		},
	}

	for _, name := range claimNames {
		pod.Spec.Volumes = append(pod.Spec.Volumes,
			corev1.Volume{
				Name: name,
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: name,
					},
				},
			})
	}

	return pod
}

func addEphemeralVolume(pod *corev1.Pod, name, storageClassName, storageQuantity string) *corev1.Pod {
	requests := corev1.ResourceList{}
	if storageQuantity != "" {
		requests[corev1.ResourceStorage] = resource.MustParse(storageQuantity)
	}

	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name: name,
		VolumeSource: corev1.VolumeSource{
			Ephemeral: &corev1.EphemeralVolumeSource{
				VolumeClaimTemplate: &corev1.PersistentVolumeClaimTemplate{
					Spec: corev1.PersistentVolumeClaimSpec{
						StorageClassName: &storageClassName,
						Resources: corev1.VolumeResourceRequirements{
							Requests: requests,
						},
					},
				},
			},
		},
	})
	return pod
}

func resourceList(name corev1.ResourceName, quantity string) corev1.ResourceList {
	return corev1.ResourceList{name: resource.MustParse(quantity)}
}

func buildPVC(name string, storageClassName string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNamespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &storageClassName,
		},
	}
}

func buildPVCWithStorage(name, storageClassName, storageQuantity string) *corev1.PersistentVolumeClaim {
	pvc := buildPVC(name, storageClassName)
	quantity := resource.MustParse(storageQuantity)
	pvc.Spec.Resources.Requests = corev1.ResourceList{}
	pvc.Spec.Resources.Requests["storage"] = quantity
	return pvc
}
