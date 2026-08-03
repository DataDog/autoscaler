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
	ca_context "k8s.io/autoscaler/cluster-autoscaler/context"
	coretest "k8s.io/autoscaler/cluster-autoscaler/core/test"
	"k8s.io/autoscaler/cluster-autoscaler/provisioningrequest/conditions"
	provreqpods "k8s.io/autoscaler/cluster-autoscaler/provisioningrequest/pods"
	"k8s.io/autoscaler/cluster-autoscaler/provisioningrequest/provreqclient"
	"k8s.io/autoscaler/cluster-autoscaler/provisioningrequest/provreqwrapper"
	"k8s.io/autoscaler/cluster-autoscaler/simulator/clustersnapshot"
	"k8s.io/autoscaler/cluster-autoscaler/simulator/scheduling"
	testutils "k8s.io/autoscaler/cluster-autoscaler/utils/test"
	resourcelisters "k8s.io/client-go/listers/resource/v1"
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

func TestBookPodsSchedulingErrorRevertsBeforeCheckCapacityBooking(t *testing.T) {
	ordinaryPr := ordinaryBookingProvisioningRequest("ordinary-pr")
	checkCapacityPr := checkCapacityProvisioningRequestWithTemplateClaim("gpu-template")
	checkCapacityPr.Spec.PodSets[0].Count = 1
	injector := &transactionalBookingInjector{}
	template := &resourcev1.ResourceClaimTemplate{ObjectMeta: metav1.ObjectMeta{Name: "gpu-template", Namespace: "ns"}}
	processor, autoscalingCtx := newTransactionalBookingProcessor(t, ordinaryPr, templateLister(t, template), injector)
	processor.checkCapacityProcessorInstance = ""
	node := testutils.BuildTestNode("node", 10000, 10000)
	require.NoError(t, autoscalingCtx.ClusterSnapshot.SetClusterState([]*corev1.Node{node}, nil, nil, nil))

	injector.schedule = func(snapshot clustersnapshot.ClusterSnapshot, pods []*corev1.Pod) ([]scheduling.Status, int, error) {
		switch injector.calls {
		case 1:
			require.Len(t, pods, 1)
			assert.Equal(t, ordinaryPr.Name, pods[0].Annotations[v1.ProvisioningRequestPodAnnotationKey])
			require.NoError(t, snapshot.SchedulePod(pods[0], node.Name))
			return allPodsScheduled(pods), 0, errors.New("ordinary booking failed")
		case 2:
			nodeInfos, err := snapshot.ListNodeInfos()
			require.NoError(t, err)
			require.Len(t, nodeInfos, 1)
			assert.Empty(t, nodeInfos[0].Pods(), "failed ordinary booking leaked into the next request")
			claims, err := snapshot.DraSnapshot().ResourceClaims().List()
			require.NoError(t, err)
			require.Len(t, claims, 1, "check-capacity claims must be added after rollback")
			require.NoError(t, snapshot.SchedulePod(pods[0], node.Name))
			return allPodsScheduled(pods), 0, nil
		default:
			t.Fatalf("unexpected scheduling call %d", injector.calls)
			return nil, 0, nil
		}
	}

	err := processor.bookCapacity(autoscalingCtx)
	require.ErrorContains(t, err, "ordinary booking failed")
	nodeInfos, err := autoscalingCtx.ClusterSnapshot.ListNodeInfos()
	require.NoError(t, err)
	require.Len(t, nodeInfos, 1)
	assert.Empty(t, nodeInfos[0].Pods(), "failed ordinary booking was not rolled back")

	require.NoError(t, processor.bookCheckCapacityProvisioningRequest(autoscalingCtx, checkCapacityPr))
	assert.Equal(t, 2, injector.calls)
	claims, err := autoscalingCtx.ClusterSnapshot.DraSnapshot().ResourceClaims().List()
	require.NoError(t, err)
	assert.Len(t, claims, 1)
	nodeInfos, err = autoscalingCtx.ClusterSnapshot.ListNodeInfos()
	require.NoError(t, err)
	require.Len(t, nodeInfos, 1)
	require.Len(t, nodeInfos[0].Pods(), 1)
	assert.Equal(t, checkCapacityPr.Name, nodeInfos[0].Pods()[0].Pod.Annotations[v1.ProvisioningRequestPodAnnotationKey])
}

func TestBookPodsCommitsSuccessfulScheduling(t *testing.T) {
	pr := ordinaryBookingProvisioningRequest("ordinary-pr")
	injector := &transactionalBookingInjector{}
	processor, autoscalingCtx := newTransactionalBookingProcessor(t, pr, templateLister(t), injector)
	processor.checkCapacityProcessorInstance = ""
	node := testutils.BuildTestNode("node", 10000, 10000)
	require.NoError(t, autoscalingCtx.ClusterSnapshot.SetClusterState([]*corev1.Node{node}, nil, nil, nil))
	injector.schedule = func(snapshot clustersnapshot.ClusterSnapshot, pods []*corev1.Pod) ([]scheduling.Status, int, error) {
		require.NoError(t, snapshot.SchedulePod(pods[0], node.Name))
		return allPodsScheduled(pods), 0, nil
	}

	require.NoError(t, processor.bookCapacity(autoscalingCtx))
	nodeInfos, err := autoscalingCtx.ClusterSnapshot.ListNodeInfos()
	require.NoError(t, err)
	require.Len(t, nodeInfos, 1)
	require.Len(t, nodeInfos[0].Pods(), 1)
	assert.Equal(t, pr.Name, nodeInfos[0].Pods()[0].Pod.Annotations[v1.ProvisioningRequestPodAnnotationKey])
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

type transactionalBookingInjector struct {
	calls    int
	schedule func(clustersnapshot.ClusterSnapshot, []*corev1.Pod) ([]scheduling.Status, int, error)
}

func (i *transactionalBookingInjector) TrySchedulePods(snapshot clustersnapshot.ClusterSnapshot, pods []*corev1.Pod, _ bool, _ clustersnapshot.SchedulingOptions) ([]scheduling.Status, int, error) {
	i.calls++
	if i.schedule != nil {
		return i.schedule(snapshot, pods)
	}
	return allPodsScheduled(pods), 0, nil
}

func allPodsScheduled(pods []*corev1.Pod) []scheduling.Status {
	statuses := make([]scheduling.Status, 0, len(pods))
	for _, pod := range pods {
		statuses = append(statuses, scheduling.Status{Pod: pod, NodeName: "node"})
	}
	return statuses
}

func ordinaryBookingProvisioningRequest(name string) *provreqwrapper.ProvisioningRequest {
	pr := provreqclient.ProvisioningRequestWrapperForTesting("ns", name)
	pr.Spec.ProvisioningClassName = v1.ProvisioningClassBestEffortAtomicScaleUp
	conditions.AddOrUpdateCondition(pr, v1.Provisioned, metav1.ConditionTrue, conditions.CapacityIsFoundReason, conditions.CapacityIsFoundMsg, metav1.Now())
	return pr
}

func newTransactionalBookingProcessor(t *testing.T, pr *provreqwrapper.ProvisioningRequest, templateLister resourcelisters.ResourceClaimTemplateLister, injector *transactionalBookingInjector) (*provReqProcessor, *ca_context.AutoscalingContext) {
	t.Helper()
	client := provreqclient.NewFakeProvisioningRequestClient(context.Background(), t, pr)
	autoscalingCtx, err := coretest.NewScaleTestAutoscalingContext(config.AutoscalingOptions{}, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err)
	return &provReqProcessor{
		now:                            time.Now,
		maxUpdated:                     20,
		client:                         client,
		injector:                       injector,
		checkCapacityProcessorInstance: "checkcap-instance",
		simulationWorkloadBuilder:      provreqpods.NewSimulationWorkloadBuilder(templateLister),
	}, &autoscalingCtx
}
