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
	autoscalingCtx, _ := NewScaleTestAutoscalingContext(config.AutoscalingOptions{}, nil, nil, nil, nil, nil, nil)

	require.NoError(t, processor.bookCapacity(&autoscalingCtx))
	claims, err := autoscalingCtx.ClusterSnapshot.DraSnapshot().ResourceClaims().List()
	require.NoError(t, err)
	assert.Len(t, claims, 2)
	assert.Nil(t, apimeta.FindStatusCondition(pr.Status.Conditions, v1.Failed))
}

func TestBookCapacityRevertsClaimsWhenCompleteWorkloadDoesNotSchedule(t *testing.T) {
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
	autoscalingCtx, _ := NewScaleTestAutoscalingContext(config.AutoscalingOptions{}, nil, nil, nil, nil, nil, nil)

	require.NoError(t, processor.bookCapacity(&autoscalingCtx))
	claims, err := autoscalingCtx.ClusterSnapshot.DraSnapshot().ResourceClaims().List()
	require.NoError(t, err)
	assert.Empty(t, claims)
	failed := apimeta.FindStatusCondition(pr.Status.Conditions, v1.Failed)
	require.NotNil(t, failed)
	assert.Equal(t, conditions.FailedToBookCapacityReason, failed.Reason)
	assert.Contains(t, failed.Message, "scheduled 1 of 2 pods")
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
	autoscalingCtx, _ := NewScaleTestAutoscalingContext(config.AutoscalingOptions{}, nil, nil, nil, nil, nil, nil)
	require.NoError(t, autoscalingCtx.ClusterSnapshot.DraSnapshot().AddClaims([]*resourcev1.ResourceClaim{workload.Claims[0].DeepCopy()}))

	require.NoError(t, processor.bookCapacity(&autoscalingCtx))
	claims, err := autoscalingCtx.ClusterSnapshot.DraSnapshot().ResourceClaims().List()
	require.NoError(t, err)
	assert.Len(t, claims, 1, "the failed request fork must leave the existing snapshot claim unchanged")
	failed := apimeta.FindStatusCondition(pr.Status.Conditions, v1.Failed)
	require.NotNil(t, failed)
	assert.Equal(t, conditions.FailedToBookCapacityReason, failed.Reason)
	assert.Contains(t, failed.Message, workload.Claims[0].Name)
}

type partialBookingInjector struct{}

func (partialBookingInjector) TrySchedulePods(_ clustersnapshot.ClusterSnapshot, _ []*corev1.Pod, _ bool, _ clustersnapshot.SchedulingOptions) ([]scheduling.Status, int, error) {
	return make([]scheduling.Status, 1), 0, nil
}
