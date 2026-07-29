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
	"k8s.io/autoscaler/cluster-autoscaler/provisioningrequest"
	"k8s.io/autoscaler/cluster-autoscaler/provisioningrequest/conditions"
	provreqpods "k8s.io/autoscaler/cluster-autoscaler/provisioningrequest/pods"
	"k8s.io/autoscaler/cluster-autoscaler/provisioningrequest/provreqclient"
	"k8s.io/autoscaler/cluster-autoscaler/provisioningrequest/provreqwrapper"
	resourcelisters "k8s.io/client-go/listers/resource/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"
)

func TestGetCheckCapacityBatchCarriesMaterializedClaims(t *testing.T) {
	template := &resourcev1.ResourceClaimTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-template", Namespace: "ns"},
	}
	pr := checkCapacityProvisioningRequestWithTemplateClaim("gpu-template")
	client := provreqclient.NewFakeProvisioningRequestClient(context.Background(), t, pr)
	builder := provreqpods.NewSimulationWorkloadBuilder(templateLister(t, template))
	injector := NewProvisioningRequestPodsInjector(client, time.Minute, 10*time.Minute, 100, true, "checkcap-instance", builder)

	batch, err := injector.GetCheckCapacityBatch(1)
	require.NoError(t, err)
	require.Len(t, batch, 1)
	require.NotNil(t, batch[0].Workload)
	require.Len(t, batch[0].Workload.Pods, 2)
	require.Len(t, batch[0].Workload.Claims, 2)
	assert.NotEqual(t, batch[0].Workload.Claims[0].Name, batch[0].Workload.Claims[1].Name)
	for i := range batch[0].Workload.Pods {
		require.Len(t, batch[0].Workload.Pods[i].Status.ResourceClaimStatuses, 1)
		assert.Equal(t, batch[0].Workload.Claims[i].Name, *batch[0].Workload.Pods[i].Status.ResourceClaimStatuses[0].ResourceClaimName)
	}
}

func TestGetCheckCapacityBatchMarksMissingTemplateFailed(t *testing.T) {
	pr := checkCapacityProvisioningRequestWithTemplateClaim("missing-template")
	client := provreqclient.NewFakeProvisioningRequestClient(context.Background(), t, pr)
	builder := provreqpods.NewSimulationWorkloadBuilder(templateLister(t))
	injector := NewProvisioningRequestPodsInjector(client, time.Minute, 10*time.Minute, 100, true, "checkcap-instance", builder)

	batch, err := injector.GetCheckCapacityBatch(1)
	require.NoError(t, err)
	assert.Empty(t, batch)
	updatedPr, err := client.ProvisioningRequestNoCache(pr.Namespace, pr.Name)
	require.NoError(t, err)
	failed := apimeta.FindStatusCondition(updatedPr.Status.Conditions, v1.Failed)
	require.NotNil(t, failed)
	assert.Equal(t, conditions.FailedToCreatePodsReason, failed.Reason)
	assert.Contains(t, failed.Message, "missing-template")
}

func checkCapacityProvisioningRequestWithTemplateClaim(templateName string) *provreqwrapper.ProvisioningRequest {
	pr := provreqclient.ProvisioningRequestWrapperForTesting("ns", "test-pr")
	pr.Spec.ProvisioningClassName = v1.ProvisioningClassCheckCapacity
	pr.Spec.Parameters = map[string]v1.Parameter{
		provisioningrequest.CheckCapacityProcessorInstanceKey: "checkcap-instance",
	}
	pr.Spec.PodSets[0].Count = 2
	pr.PodTemplates[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
		{Name: "gpu", ResourceClaimTemplateName: ptr.To(templateName)},
	}
	return pr
}

func templateLister(t *testing.T, templates ...*resourcev1.ResourceClaimTemplate) resourcelisters.ResourceClaimTemplateLister {
	t.Helper()
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	for _, template := range templates {
		require.NoError(t, indexer.Add(template))
	}
	return resourcelisters.NewResourceClaimTemplateLister(indexer)
}
