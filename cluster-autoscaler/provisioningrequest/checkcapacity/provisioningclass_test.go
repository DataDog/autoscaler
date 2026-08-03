/*
Copyright 2024 The Kubernetes Authors.

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

package checkcapacity

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1"
	"k8s.io/autoscaler/cluster-autoscaler/config"
	ca_context "k8s.io/autoscaler/cluster-autoscaler/context"
	coretest "k8s.io/autoscaler/cluster-autoscaler/core/test"
	"k8s.io/autoscaler/cluster-autoscaler/processors/provreq"
	"k8s.io/autoscaler/cluster-autoscaler/processors/status"
	"k8s.io/autoscaler/cluster-autoscaler/provisioningrequest/conditions"
	provreqpods "k8s.io/autoscaler/cluster-autoscaler/provisioningrequest/pods"
	"k8s.io/autoscaler/cluster-autoscaler/provisioningrequest/provreqclient"
	"k8s.io/autoscaler/cluster-autoscaler/simulator/clustersnapshot"
	"k8s.io/autoscaler/cluster-autoscaler/simulator/scheduling"
	"k8s.io/autoscaler/cluster-autoscaler/utils/errors"
	resourcelisters "k8s.io/client-go/listers/resource/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"
)

func TestCombinedStatusSet(t *testing.T) {
	// TestCombinedStatusSet tests the CombinedStatusSet function.
	testCases := []struct {
		name          string
		statuses      []*status.ScaleUpStatus
		exportedResut status.ScaleUpResult
		exportedError errors.AutoscalerError
		returnedError errors.AutoscalerError
	}{
		{
			name:          "empty",
			statuses:      []*status.ScaleUpStatus{},
			exportedResut: status.ScaleUpNotTried,
		},
		{
			name:          "all successful",
			statuses:      generateStatuses(2, status.ScaleUpSuccessful),
			exportedResut: status.ScaleUpSuccessful,
		},
		{
			name:          "all errors",
			statuses:      generateStatuses(2, status.ScaleUpError),
			exportedResut: status.ScaleUpError,
			exportedError: errors.NewAutoscalerError(errors.InternalError, "error 0 ...and other concurrent errors: [\"error 1\"]"),
			returnedError: errors.NewAutoscalerError(errors.InternalError, "error 0 ...and other concurrent errors: [\"error 1\"]"),
		},
		{
			name:          "all no options available",
			statuses:      generateStatuses(2, status.ScaleUpNoOptionsAvailable),
			exportedResut: status.ScaleUpNoOptionsAvailable,
		},
		{
			name:          "error and successful",
			statuses:      append(generateStatuses(1, status.ScaleUpError), generateStatuses(1, status.ScaleUpSuccessful)...),
			exportedResut: status.ScaleUpSuccessful,
			exportedError: errors.NewAutoscalerError(errors.InternalError, "error 0"),
		},
		{
			name:          "error and no options available",
			statuses:      append(generateStatuses(1, status.ScaleUpError), generateStatuses(1, status.ScaleUpNoOptionsAvailable)...),
			exportedResut: status.ScaleUpError,
			exportedError: errors.NewAutoscalerError(errors.InternalError, "error 0"),
			returnedError: errors.NewAutoscalerError(errors.InternalError, "error 0"),
		},
		{
			name:          "successful and no options available",
			statuses:      append(generateStatuses(1, status.ScaleUpSuccessful), generateStatuses(1, status.ScaleUpNoOptionsAvailable)...),
			exportedResut: status.ScaleUpSuccessful,
		},
		{
			name:          "error, successful and no options available",
			statuses:      append(generateStatuses(1, status.ScaleUpNoOptionsAvailable), append(generateStatuses(1, status.ScaleUpError), generateStatuses(1, status.ScaleUpSuccessful)...)...),
			exportedResut: status.ScaleUpSuccessful,
			exportedError: errors.NewAutoscalerError(errors.InternalError, "error 0"),
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			combinedStatus := NewCombinedStatusSet()

			for _, s := range tc.statuses {
				combinedStatus.Add(s)
			}

			export, retErr := combinedStatus.Export()

			assert.Equal(t, export.Result, tc.exportedResut)

			if tc.exportedError == nil {
				assert.Nil(t, export.ScaleUpError)
			} else {
				assert.Equal(t, tc.exportedError.Error(), (*export.ScaleUpError).Error())
			}

			if tc.returnedError == nil {
				assert.Nil(t, retErr)
			} else {
				assert.Equal(t, tc.returnedError.Error(), retErr.Error())
			}
		})
	}
}

func generateStatuses(n int, result status.ScaleUpResult) []*status.ScaleUpStatus {
	// generateStatuses generates n statuses with the given result.
	statuses := make([]*status.ScaleUpStatus, n)
	for i := 0; i < n; i++ {
		var scaleUpErr *errors.AutoscalerError

		if result == status.ScaleUpError {
			newErr := errors.NewAutoscalerError(errors.InternalError, fmt.Sprintf("error %d", i))
			scaleUpErr = &newErr
		}

		statuses[i] = &status.ScaleUpStatus{Result: result, ScaleUpError: scaleUpErr}
	}
	return statuses
}

func TestGetProvisioningRequestsAndPodsNonBatchIgnoresUnrelatedRCTPod(t *testing.T) {
	const (
		namespace    = "test-ns"
		templateName = "gpu-template"
	)
	pr := provreqclient.ProvisioningRequestWrapperForTesting(namespace, "test-pr")
	pr.Spec.ProvisioningClassName = v1.ProvisioningClassCheckCapacity
	pr.PodTemplates[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
		{Name: "gpu", ResourceClaimTemplateName: ptr.To(templateName)},
	}
	virtualPods, err := provreqpods.PodsForProvisioningRequest(pr)
	require.NoError(t, err)

	realClaimName := "real-pod-gpu-controller-suffix"
	realPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "real-pod", Namespace: namespace},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "container", Image: "image"}},
			ResourceClaims: []corev1.PodResourceClaim{
				{Name: "gpu", ResourceClaimTemplateName: ptr.To(templateName)},
			},
		},
		Status: corev1.PodStatus{
			ResourceClaimStatuses: []corev1.PodResourceClaimStatus{
				{Name: "gpu", ResourceClaimName: ptr.To(realClaimName)},
			},
		},
	}
	realPodBefore := realPod.DeepCopy()

	template := &resourcev1.ResourceClaimTemplate{ObjectMeta: metav1.ObjectMeta{Name: templateName, Namespace: namespace}}
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	require.NoError(t, indexer.Add(template))
	checkCapacityClass := &checkCapacityProvClass{
		autoscalingCtx: &ca_context.AutoscalingContext{},
		client:         provreqclient.NewFakeProvisioningRequestClient(context.Background(), t, pr),
		simulationWorkloadBuilder: provreqpods.NewSimulationWorkloadBuilder(
			resourcelisters.NewResourceClaimTemplateLister(indexer),
		),
	}

	requests, err := checkCapacityClass.getProvisioningRequestsAndPods(append(virtualPods, realPod))
	require.NoError(t, err)
	require.Len(t, requests, 1)
	assert.Equal(t, pr.Name, requests[0].PrWrapper.Name)
	require.NoError(t, requests[0].Err)
	require.NotNil(t, requests[0].Workload)
	require.Len(t, requests[0].Workload.Pods, len(virtualPods))
	require.Len(t, requests[0].Workload.Claims, len(virtualPods))
	for _, pod := range requests[0].Workload.Pods {
		assert.Equal(t, pr.Name, pod.Annotations[v1.ProvisioningRequestPodAnnotationKey])
		assert.NotEqual(t, realPod.Name, pod.Name)
	}
	assert.Equal(t, realPodBefore, realPod, "the unrelated real Pod was mutated")
}

func TestCheckCapacityBatchMarksMaterializationErrorsFailed(t *testing.T) {
	pr := provreqclient.ProvisioningRequestWrapperForTesting("test-ns", "test-pr")
	combinedStatus := NewCombinedStatusSet()
	checkCapacityClass := &checkCapacityProvClass{}

	updates := checkCapacityClass.checkCapacityBatch(
		[]provreq.ProvisioningRequestWithPods{
			{
				PrWrapper: pr,
				Err:       fmt.Errorf("ResourceClaimTemplate test-ns/missing was not found"),
			},
		},
		&combinedStatus,
		time.Now(),
	)

	require.Len(t, updates, 1)
	failed := apimeta.FindStatusCondition(pr.Status.Conditions, v1.Failed)
	require.NotNil(t, failed)
	assert.Equal(t, metav1.ConditionTrue, failed.Status)
	assert.Equal(t, conditions.FailedToCreatePodsReason, failed.Reason)
	assert.Contains(t, failed.Message, "ResourceClaimTemplate")
	exported, err := combinedStatus.Export()
	require.Error(t, err)
	assert.Equal(t, status.ScaleUpError, exported.Result)
}

func TestCheckCapacityBatchDoesNotReportInternalSchedulingErrorsAsNoCapacity(t *testing.T) {
	pr := provreqclient.ProvisioningRequestWrapperForTesting("test-ns", "test-pr")
	pr.Spec.Parameters = map[string]v1.Parameter{NoRetryParameterKey: "true"}
	autoscalingCtx, err := coretest.NewScaleTestAutoscalingContext(config.AutoscalingOptions{}, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err)
	autoscalingCtx.ClusterSnapshot = &internalErrorSnapshot{ClusterSnapshot: autoscalingCtx.ClusterSnapshot}

	checkCapacityClass := &checkCapacityProvClass{
		autoscalingCtx:      &autoscalingCtx,
		schedulingSimulator: scheduling.NewHintingSimulator(),
	}
	combinedStatus := NewCombinedStatusSet()
	updates := checkCapacityClass.checkCapacityBatch(
		[]provreq.ProvisioningRequestWithPods{
			{
				PrWrapper: pr,
				Workload: &provreqpods.SimulationWorkload{
					Pods: []*corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "virtual-pod", Namespace: "test-ns"}}},
				},
			},
		},
		&combinedStatus,
		time.Now(),
	)

	require.Len(t, updates, 1)
	assert.Nil(t, apimeta.FindStatusCondition(pr.Status.Conditions, v1.Failed))
	assert.Nil(t, apimeta.FindStatusCondition(pr.Status.Conditions, v1.Provisioned))
	exported, err := combinedStatus.Export()
	require.ErrorContains(t, err, "scheduling failed")
	assert.Equal(t, status.ScaleUpError, exported.Result)
}

type internalErrorSnapshot struct {
	clustersnapshot.ClusterSnapshot
}

func (s *internalErrorSnapshot) SchedulePodOnAnyNodeMatching(pod *corev1.Pod, _ clustersnapshot.SchedulingOptions) (string, clustersnapshot.SchedulingError) {
	return "", clustersnapshot.NewSchedulingInternalError(pod, "scheduling failed")
}
