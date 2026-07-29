/*
Copyright The Kubernetes Authors.

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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/autoscaler/cluster-autoscaler/simulator/clustersnapshot/testsnapshot"
	csisnapshot "k8s.io/autoscaler/cluster-autoscaler/simulator/csi/snapshot"
	drasnapshot "k8s.io/autoscaler/cluster-autoscaler/simulator/dynamicresources/snapshot"
	testutils "k8s.io/autoscaler/cluster-autoscaler/utils/test"
	resourcelisters "k8s.io/client-go/listers/resource/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"
)

func TestSimulationWorkloadBuilderForPodsMaterializesTemplateClaims(t *testing.T) {
	template := &resourcev1.ResourceClaimTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-template", Namespace: "test-ns"},
		Spec: resourcev1.ResourceClaimTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels:      map[string]string{"template-label": "true"},
				Annotations: map[string]string{"template-annotation": "true"},
			},
			Spec: resourcev1.ResourceClaimSpec{},
		},
	}
	builder := NewSimulationWorkloadBuilder(resourceClaimTemplateLister(t, template))
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      strings.Repeat("a", 245),
			Namespace: "test-ns",
			UID:       types.UID("test-ns/virtual-pod"),
		},
		Spec: corev1.PodSpec{
			ResourceClaims: []corev1.PodResourceClaim{
				{Name: "gpu", ResourceClaimTemplateName: ptr.To("gpu-template")},
				{Name: "shared", ResourceClaimName: ptr.To("shared-claim")},
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodPending},
	}

	workload, err := builder.ForPods([]*corev1.Pod{pod})
	require.NoError(t, err)
	require.Len(t, workload.Pods, 1)
	require.Len(t, workload.Claims, 1)

	materializedPod := workload.Pods[0]
	claim := workload.Claims[0]
	assert.NotSame(t, pod, materializedPod)
	assert.Equal(t, corev1.PodPending, materializedPod.Status.Phase)
	require.Len(t, materializedPod.Status.ResourceClaimStatuses, 1)
	assert.Equal(t, "gpu", materializedPod.Status.ResourceClaimStatuses[0].Name)
	require.NotNil(t, materializedPod.Status.ResourceClaimStatuses[0].ResourceClaimName)
	assert.Equal(t, claim.Name, *materializedPod.Status.ResourceClaimStatuses[0].ResourceClaimName)
	assert.LessOrEqual(t, len(claim.Name), maxObjectNameLength)
	assert.Regexp(t, `-[0-9a-f]{16}$`, claim.Name)
	assert.Equal(t, pod.Namespace, claim.Namespace)
	assert.Equal(t, types.UID(pod.Namespace+"/"+claim.Name), claim.UID)
	assert.Equal(t, template.Spec.Spec, claim.Spec)
	assert.Equal(t, "true", claim.Labels["template-label"])
	assert.Equal(t, "true", claim.Annotations["template-annotation"])
	assert.Equal(t, "gpu", claim.Annotations[resourcev1.PodResourceClaimAnnotation])
	require.Len(t, claim.OwnerReferences, 1)
	assert.Equal(t, pod.Name, claim.OwnerReferences[0].Name)
	assert.Equal(t, pod.UID, claim.OwnerReferences[0].UID)
	assert.Equal(t, "Pod", claim.OwnerReferences[0].Kind)
	assert.True(t, ptr.Deref(claim.OwnerReferences[0].Controller, false))
	assert.Nil(t, claim.OwnerReferences[0].BlockOwnerDeletion)

	assert.Empty(t, pod.Status.ResourceClaimStatuses, "input Pod must not be mutated")
	assert.NotContains(t, template.Spec.Annotations, resourcev1.PodResourceClaimAnnotation, "cached template must not be mutated")

	repeated, err := builder.ForPods(workload.Pods)
	require.NoError(t, err)
	require.Len(t, repeated.Claims, 1)
	assert.Equal(t, claim.Name, repeated.Claims[0].Name)
	assert.Equal(t, materializedPod.Status.ResourceClaimStatuses, repeated.Pods[0].Status.ResourceClaimStatuses)
}

func TestSimulationWorkloadBuilderForPodsCreatesUniqueClaimsPerPod(t *testing.T) {
	gpuTemplate := &resourcev1.ResourceClaimTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-template", Namespace: "test-ns"},
	}
	networkTemplate := &resourcev1.ResourceClaimTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "network-template", Namespace: "test-ns"},
	}
	builder := NewSimulationWorkloadBuilder(resourceClaimTemplateLister(t, gpuTemplate, networkTemplate))
	pods := []*corev1.Pod{
		virtualPodWithTemplateClaim("pod-0", "uid-0", "gpu-template"),
		virtualPodWithTemplateClaim("pod-1", "uid-1", "gpu-template"),
	}
	pods[0].Spec.ResourceClaims = append(pods[0].Spec.ResourceClaims, corev1.PodResourceClaim{
		Name:                      "network",
		ResourceClaimTemplateName: ptr.To("network-template"),
	})

	workload, err := builder.ForPods(pods)
	require.NoError(t, err)
	require.Len(t, workload.Claims, 3)
	assert.NotEqual(t, workload.Claims[0].Name, workload.Claims[2].Name)
	assert.Equal(t, workload.Claims[0].Name, *workload.Pods[0].Status.ResourceClaimStatuses[0].ResourceClaimName)
	assert.Equal(t, workload.Claims[1].Name, *workload.Pods[0].Status.ResourceClaimStatuses[1].ResourceClaimName)
	assert.Equal(t, workload.Claims[2].Name, *workload.Pods[1].Status.ResourceClaimStatuses[0].ResourceClaimName)
}

func TestSimulationWorkloadBuilderForPodsPassesDirectClaimsThrough(t *testing.T) {
	builder := NewSimulationWorkloadBuilder(nil)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "test-ns", UID: types.UID("uid")},
		Spec: corev1.PodSpec{
			ResourceClaims: []corev1.PodResourceClaim{
				{Name: "shared", ResourceClaimName: ptr.To("shared-claim")},
			},
		},
	}

	workload, err := builder.ForPods([]*corev1.Pod{pod})
	require.NoError(t, err)
	assert.Empty(t, workload.Claims)
	require.Len(t, workload.Pods, 1)
	assert.Equal(t, pod.Spec.ResourceClaims, workload.Pods[0].Spec.ResourceClaims)
	assert.Empty(t, workload.Pods[0].Status.ResourceClaimStatuses)
}

func TestSimulationWorkloadBuilderForPodsRejectsInvalidClaims(t *testing.T) {
	template := &resourcev1.ResourceClaimTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-template", Namespace: "test-ns"},
	}
	builder := NewSimulationWorkloadBuilder(resourceClaimTemplateLister(t, template))
	expectedName := simulationResourceClaimName(virtualPodWithTemplateClaim("pod", "uid", "gpu-template"), "gpu")

	tests := []struct {
		name    string
		claims  []corev1.PodResourceClaim
		status  []corev1.PodResourceClaimStatus
		wantErr string
	}{
		{
			name:    "empty logical name",
			claims:  []corev1.PodResourceClaim{{ResourceClaimTemplateName: ptr.To("gpu-template")}},
			wantErr: "empty logical name",
		},
		{
			name:    "missing template",
			claims:  []corev1.PodResourceClaim{{Name: "gpu", ResourceClaimTemplateName: ptr.To("missing")}},
			wantErr: "could not get ResourceClaimTemplate",
		},
		{
			name:    "empty template name",
			claims:  []corev1.PodResourceClaim{{Name: "gpu", ResourceClaimTemplateName: ptr.To("")}},
			wantErr: "empty resourceClaimTemplateName",
		},
		{
			name:    "empty direct claim name",
			claims:  []corev1.PodResourceClaim{{Name: "gpu", ResourceClaimName: ptr.To("")}},
			wantErr: "empty resourceClaimName",
		},
		{
			name: "duplicate logical name",
			claims: []corev1.PodResourceClaim{
				{Name: "gpu", ResourceClaimTemplateName: ptr.To("gpu-template")},
				{Name: "gpu", ResourceClaimTemplateName: ptr.To("gpu-template")},
			},
			wantErr: "duplicate logical resource claim",
		},
		{
			name:    "no source",
			claims:  []corev1.PodResourceClaim{{Name: "gpu"}},
			wantErr: "has no resource claim source",
		},
		{
			name:    "both sources",
			claims:  []corev1.PodResourceClaim{{Name: "gpu", ResourceClaimName: ptr.To("direct"), ResourceClaimTemplateName: ptr.To("gpu-template")}},
			wantErr: "sets both",
		},
		{
			name:    "conflicting status",
			claims:  []corev1.PodResourceClaim{{Name: "gpu", ResourceClaimTemplateName: ptr.To("gpu-template")}},
			status:  []corev1.PodResourceClaimStatus{{Name: "gpu", ResourceClaimName: ptr.To(expectedName + "-different")}},
			wantErr: "conflicting status mapping",
		},
		{
			name:   "duplicate identical status",
			claims: []corev1.PodResourceClaim{{Name: "gpu", ResourceClaimTemplateName: ptr.To("gpu-template")}},
			status: []corev1.PodResourceClaimStatus{
				{Name: "gpu", ResourceClaimName: ptr.To(expectedName)},
				{Name: "gpu", ResourceClaimName: ptr.To(expectedName)},
			},
			wantErr: "conflicting status mapping",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "test-ns", UID: types.UID("uid")},
				Spec:       corev1.PodSpec{ResourceClaims: test.claims},
				Status:     corev1.PodStatus{ResourceClaimStatuses: test.status},
			}
			_, err := builder.ForPods([]*corev1.Pod{pod})
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.wantErr)
		})
	}
}

func TestSimulationWorkloadBuilderForPodsRequiresAssignedPodIdentity(t *testing.T) {
	builder := NewSimulationWorkloadBuilder(nil)
	for _, pod := range []*corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "pod", UID: types.UID("uid")}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "test-ns", UID: types.UID("uid")}},
		{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "test-ns"}},
	} {
		_, err := builder.ForPods([]*corev1.Pod{pod})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must have namespace, name, and UID")
	}
}

func TestSimulationWorkloadMaterializedClaimSchedulesInDraSnapshot(t *testing.T) {
	template := &resourcev1.ResourceClaimTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-template", Namespace: "default"},
		Spec: resourcev1.ResourceClaimTemplateSpec{
			Spec: resourcev1.ResourceClaimSpec{
				Devices: resourcev1.DeviceClaim{
					Requests: []resourcev1.DeviceRequest{
						{
							Name: "gpu",
							Exactly: &resourcev1.ExactDeviceRequest{
								DeviceClassName: "gpu.example.com",
								AllocationMode:  resourcev1.DeviceAllocationModeExactCount,
								Count:           1,
							},
						},
					},
				},
			},
		},
	}
	pod := testutils.BuildTestPod("virtual-pod", 100, 100)
	pod.Spec.ResourceClaims = []corev1.PodResourceClaim{
		{Name: "gpu", ResourceClaimTemplateName: ptr.To(template.Name)},
	}
	workload, err := NewSimulationWorkloadBuilder(resourceClaimTemplateLister(t, template)).ForPods([]*corev1.Pod{pod})
	require.NoError(t, err)

	node := testutils.BuildTestNode("node", 1000, 1000)
	resourceSlice := &resourcev1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "node-gpus"},
		Spec: resourcev1.ResourceSliceSpec{
			NodeName: ptr.To(node.Name),
			Driver:   "gpu.example.com",
			Pool: resourcev1.ResourcePool{
				Name:               "node-gpus",
				ResourceSliceCount: 1,
			},
			Devices: []resourcev1.Device{{Name: "gpu-0"}},
		},
	}
	draState := drasnapshot.NewSnapshot(
		nil,
		map[string][]*resourcev1.ResourceSlice{node.Name: {resourceSlice}},
		nil,
		map[string]*resourcev1.DeviceClass{
			"gpu.example.com": {
				ObjectMeta: metav1.ObjectMeta{Name: "gpu.example.com"},
			},
		},
	)
	clusterSnapshot := testsnapshot.NewTestSnapshotOrDie(t)
	require.NoError(t, clusterSnapshot.SetClusterState(
		[]*corev1.Node{node},
		nil,
		draState,
		csisnapshot.NewEmptySnapshot(),
	))

	clusterSnapshot.Fork()
	require.NoError(t, clusterSnapshot.DraSnapshot().AddClaims(workload.Claims))
	require.NoError(t, clusterSnapshot.SchedulePod(workload.Pods[0], node.Name))
	require.NoError(t, clusterSnapshot.Commit())

	claims, err := clusterSnapshot.DraSnapshot().ResourceClaims().List()
	require.NoError(t, err)
	require.Len(t, claims, 1)
	assert.NotNil(t, claims[0].Status.Allocation)
	assert.NotEmpty(t, claims[0].Status.ReservedFor)
}

func virtualPodWithTemplateClaim(name, uid, templateName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test-ns", UID: types.UID(uid)},
		Spec: corev1.PodSpec{
			ResourceClaims: []corev1.PodResourceClaim{
				{Name: "gpu", ResourceClaimTemplateName: ptr.To(templateName)},
			},
		},
	}
}

func resourceClaimTemplateLister(t *testing.T, templates ...*resourcev1.ResourceClaimTemplate) resourcelisters.ResourceClaimTemplateLister {
	t.Helper()
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	for _, template := range templates {
		require.NoError(t, indexer.Add(template))
	}
	return resourcelisters.NewResourceClaimTemplateLister(indexer)
}
