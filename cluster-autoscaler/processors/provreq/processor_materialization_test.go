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

package provreq

import (
	"context"
	"errors"
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
	coretest "k8s.io/autoscaler/cluster-autoscaler/core/test"
	"k8s.io/autoscaler/cluster-autoscaler/provisioningrequest/conditions"
	provreqpods "k8s.io/autoscaler/cluster-autoscaler/provisioningrequest/pods"
	"k8s.io/autoscaler/cluster-autoscaler/provisioningrequest/provreqclient"
	"k8s.io/autoscaler/cluster-autoscaler/simulator/clustersnapshot"
	"k8s.io/autoscaler/cluster-autoscaler/simulator/scheduling"
)

func TestBookCapacityAddsMaterializedClaimsToSnapshot(t *testing.T) {
	template := &resourcev1.ResourceClaimTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-template", Namespace: "ns"},
	}
	pr := checkCapacityProvisioningRequestWithTemplateClaim("gpu-template")
	conditions.AddOrUpdateCondition(pr, v1.Provisioned, metav1.ConditionTrue, conditions.CapacityIsFoundReason, conditions.CapacityIsFoundMsg, metav1.Now())
	client := provreqclient.NewFakeProvisioningRequestClient(context.Background(), t, pr)
	builder := provreqpods.NewSimulationWorkloadBuilder(templateLister(t, template))
	processor := &provReqProcessor{
		now:                            time.Now,
		maxUpdated:                     20,
		client:                         client,
		injector:                       &fakeInjector{},
		checkCapacityProcessorInstance: "checkcap-instance",
		simulationWorkloadBuilder:      builder,
	}
	autoscalingCtx, _ := coretest.NewScaleTestAutoscalingContext(config.AutoscalingOptions{}, nil, nil, nil, nil, nil, nil)

	require.NoError(t, processor.bookCapacity(&autoscalingCtx))
	claims, err := autoscalingCtx.ClusterSnapshot.DraSnapshot().ResourceClaims().List()
	require.NoError(t, err)
	assert.Len(t, claims, 2)
	updatedPr, err := client.ProvisioningRequestNoCache(pr.Namespace, pr.Name)
	require.NoError(t, err)
	assert.Nil(t, apimeta.FindStatusCondition(updatedPr.Status.Conditions, v1.Failed))
}

func TestBookCapacityCommitsOnlyClaimsForPodsWhichSchedule(t *testing.T) {
	template := &resourcev1.ResourceClaimTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-template", Namespace: "ns"},
	}
	pr := checkCapacityProvisioningRequestWithTemplateClaim("gpu-template")
	conditions.AddOrUpdateCondition(pr, v1.Provisioned, metav1.ConditionTrue, conditions.CapacityIsFoundReason, conditions.CapacityIsFoundMsg, metav1.Now())
	client := provreqclient.NewFakeProvisioningRequestClient(context.Background(), t, pr)
	builder := provreqpods.NewSimulationWorkloadBuilder(templateLister(t, template))
	processor := &provReqProcessor{
		now:                            time.Now,
		maxUpdated:                     20,
		client:                         client,
		injector:                       partialBookingInjector{},
		checkCapacityProcessorInstance: "checkcap-instance",
		simulationWorkloadBuilder:      builder,
	}
	autoscalingCtx, _ := coretest.NewScaleTestAutoscalingContext(config.AutoscalingOptions{}, nil, nil, nil, nil, nil, nil)

	require.NoError(t, processor.bookCapacity(&autoscalingCtx))
	claims, err := autoscalingCtx.ClusterSnapshot.DraSnapshot().ResourceClaims().List()
	require.NoError(t, err)
	require.Len(t, claims, 1)
	assert.Equal(t, "test-pr-0-0", claims[0].OwnerReferences[0].Name)
	updatedPr, err := client.ProvisioningRequestNoCache(pr.Namespace, pr.Name)
	require.NoError(t, err)
	assert.Nil(t, apimeta.FindStatusCondition(updatedPr.Status.Conditions, v1.Failed))
}

func TestBookCapacityNoFitLeavesProvisionedRequestUnchanged(t *testing.T) {
	template := &resourcev1.ResourceClaimTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-template", Namespace: "ns"},
	}
	pr := checkCapacityProvisioningRequestWithTemplateClaim("gpu-template")
	conditions.AddOrUpdateCondition(pr, v1.Provisioned, metav1.ConditionTrue, conditions.CapacityIsFoundReason, conditions.CapacityIsFoundMsg, metav1.Now())
	client := provreqclient.NewFakeProvisioningRequestClient(context.Background(), t, pr)
	builder := provreqpods.NewSimulationWorkloadBuilder(templateLister(t, template))
	processor := &provReqProcessor{
		now:                            time.Now,
		maxUpdated:                     20,
		client:                         client,
		injector:                       noCapacityBookingInjector{},
		checkCapacityProcessorInstance: "checkcap-instance",
		simulationWorkloadBuilder:      builder,
	}
	autoscalingCtx, _ := coretest.NewScaleTestAutoscalingContext(config.AutoscalingOptions{}, nil, nil, nil, nil, nil, nil)

	require.NoError(t, processor.bookCapacity(&autoscalingCtx))
	claims, err := autoscalingCtx.ClusterSnapshot.DraSnapshot().ResourceClaims().List()
	require.NoError(t, err)
	assert.Empty(t, claims)
	updatedPr, err := client.ProvisioningRequestNoCache(pr.Namespace, pr.Name)
	require.NoError(t, err)
	assert.Nil(t, apimeta.FindStatusCondition(updatedPr.Status.Conditions, v1.Failed))
	assert.True(t, apimeta.IsStatusConditionTrue(updatedPr.Status.Conditions, v1.Provisioned))
}

func TestBookCapacitySchedulingErrorRevertsWithoutFailingRequest(t *testing.T) {
	template := &resourcev1.ResourceClaimTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-template", Namespace: "ns"},
	}
	pr := checkCapacityProvisioningRequestWithTemplateClaim("gpu-template")
	conditions.AddOrUpdateCondition(pr, v1.Provisioned, metav1.ConditionTrue, conditions.CapacityIsFoundReason, conditions.CapacityIsFoundMsg, metav1.Now())
	client := provreqclient.NewFakeProvisioningRequestClient(context.Background(), t, pr)
	builder := provreqpods.NewSimulationWorkloadBuilder(templateLister(t, template))
	processor := &provReqProcessor{
		now:                            time.Now,
		maxUpdated:                     20,
		client:                         client,
		injector:                       failingBookingInjector{},
		checkCapacityProcessorInstance: "checkcap-instance",
		simulationWorkloadBuilder:      builder,
	}
	autoscalingCtx, _ := coretest.NewScaleTestAutoscalingContext(config.AutoscalingOptions{}, nil, nil, nil, nil, nil, nil)

	err := processor.bookCapacity(&autoscalingCtx)
	require.ErrorContains(t, err, "scheduling failed")
	claims, listErr := autoscalingCtx.ClusterSnapshot.DraSnapshot().ResourceClaims().List()
	require.NoError(t, listErr)
	assert.Empty(t, claims)
	updatedPr, getErr := client.ProvisioningRequestNoCache(pr.Namespace, pr.Name)
	require.NoError(t, getErr)
	assert.Nil(t, apimeta.FindStatusCondition(updatedPr.Status.Conditions, v1.Failed))
	assert.True(t, apimeta.IsStatusConditionTrue(updatedPr.Status.Conditions, v1.Provisioned))
}

func TestBookCapacityMarksSnapshotClaimCollisionFailed(t *testing.T) {
	template := &resourcev1.ResourceClaimTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-template", Namespace: "ns"},
	}
	pr := checkCapacityProvisioningRequestWithTemplateClaim("gpu-template")
	conditions.AddOrUpdateCondition(pr, v1.Provisioned, metav1.ConditionTrue, conditions.CapacityIsFoundReason, conditions.CapacityIsFoundMsg, metav1.Now())
	client := provreqclient.NewFakeProvisioningRequestClient(context.Background(), t, pr)
	builder := provreqpods.NewSimulationWorkloadBuilder(templateLister(t, template))
	workload, err := builder.ForProvisioningRequest(pr)
	require.NoError(t, err)
	require.NotEmpty(t, workload.Claims)

	processor := &provReqProcessor{
		now:                            time.Now,
		maxUpdated:                     20,
		client:                         client,
		injector:                       &fakeInjector{},
		checkCapacityProcessorInstance: "checkcap-instance",
		simulationWorkloadBuilder:      builder,
	}
	autoscalingCtx, _ := coretest.NewScaleTestAutoscalingContext(config.AutoscalingOptions{}, nil, nil, nil, nil, nil, nil)
	require.NoError(t, autoscalingCtx.ClusterSnapshot.DraSnapshot().AddClaims([]*resourcev1.ResourceClaim{workload.Claims[0].DeepCopy()}))

	require.NoError(t, processor.bookCapacity(&autoscalingCtx))
	claims, err := autoscalingCtx.ClusterSnapshot.DraSnapshot().ResourceClaims().List()
	require.NoError(t, err)
	assert.Len(t, claims, 1, "the failed request fork must leave the existing snapshot claim unchanged")
	updatedPr, err := client.ProvisioningRequestNoCache(pr.Namespace, pr.Name)
	require.NoError(t, err)
	failed := apimeta.FindStatusCondition(updatedPr.Status.Conditions, v1.Failed)
	require.NotNil(t, failed)
	assert.Equal(t, conditions.FailedToBookCapacityReason, failed.Reason)
	assert.Contains(t, failed.Message, workload.Claims[0].Name)
}

type partialBookingInjector struct{}

func (partialBookingInjector) TrySchedulePods(_ clustersnapshot.ClusterSnapshot, pods []*corev1.Pod, _ bool, _ clustersnapshot.SchedulingOptions) ([]scheduling.Status, int, error) {
	return []scheduling.Status{{Pod: pods[0], NodeName: "node"}}, 0, nil
}

type noCapacityBookingInjector struct{}

func (noCapacityBookingInjector) TrySchedulePods(_ clustersnapshot.ClusterSnapshot, _ []*corev1.Pod, _ bool, _ clustersnapshot.SchedulingOptions) ([]scheduling.Status, int, error) {
	return nil, 0, nil
}

type failingBookingInjector struct{}

func (failingBookingInjector) TrySchedulePods(_ clustersnapshot.ClusterSnapshot, _ []*corev1.Pod, _ bool, _ clustersnapshot.SchedulingOptions) ([]scheduling.Status, int, error) {
	return nil, 0, errors.New("scheduling failed")
}
