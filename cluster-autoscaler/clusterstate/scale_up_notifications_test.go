/*
Copyright 2026 The Kubernetes Authors.

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

package clusterstate

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	mockprovider "k8s.io/autoscaler/cluster-autoscaler/cloudprovider/mocks"
	testprovider "k8s.io/autoscaler/cluster-autoscaler/cloudprovider/test"
	"k8s.io/autoscaler/cluster-autoscaler/clusterstate/scaleupfailures"
	"k8s.io/autoscaler/cluster-autoscaler/clusterstate/utils"
	"k8s.io/autoscaler/cluster-autoscaler/config"
	"k8s.io/autoscaler/cluster-autoscaler/metrics"
	"k8s.io/autoscaler/cluster-autoscaler/observers/nodegroupchange"
	"k8s.io/autoscaler/cluster-autoscaler/processors/nodegroupconfig"
	"k8s.io/autoscaler/cluster-autoscaler/simulator/framework"
	"k8s.io/autoscaler/cluster-autoscaler/utils/backoff"
	. "k8s.io/autoscaler/cluster-autoscaler/utils/test"
	"k8s.io/client-go/kubernetes/fake"
	kube_record "k8s.io/client-go/tools/record"
)

type failureNotificationObserver struct {
	nodegroupchange.NodeGroupChangeObserver
	onFailure   func(cloudprovider.NodeGroup, int, cloudprovider.InstanceErrorInfo, time.Time)
	onScaleDown func()
}

func (o *failureNotificationObserver) RegisterFailedScaleUp(group cloudprovider.NodeGroup, delta int, info cloudprovider.InstanceErrorInfo, now time.Time) {
	if o.onFailure != nil {
		o.onFailure(group, delta, info, now)
	}
}

func (o *failureNotificationObserver) RegisterScaleDown(_ cloudprovider.NodeGroup, _ string, _, _ time.Time) {
	if o.onScaleDown != nil {
		o.onScaleDown()
	}
}

type backoffDelegate interface {
	backoff.Backoff
}

type coordinatedBackoff struct {
	backoffDelegate
	updateLocked chan struct{}
	resumeUpdate chan struct{}
}

func (b *coordinatedBackoff) RemoveStaleBackoffData(now time.Time) {
	close(b.updateLocked)
	<-b.resumeUpdate
	b.backoffDelegate.RemoveStaleBackoffData(now)
}

func TestScaleUpFailureNotificationsReleaseClusterStateLock(t *testing.T) {
	for _, tc := range []struct {
		name        string
		timedOut    bool
		revertError error
		requested   int
	}{
		{name: "timeout", timedOut: true, requested: 1},
		{name: "timeout with failed target rollback", timedOut: true, revertError: fmt.Errorf("cloud provider error"), requested: 1},
		{name: "instance creation failure", requested: 1},
		{name: "partial instance creation failure", requested: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			csr, group, nodeInfo, wantError := newFailureNotificationTestRegistry(t, now, tc.timedOut, tc.revertError, tc.requested, nil)
			notifications := 0
			csr.scaleStateNotifier.Register(&failureNotificationObserver{
				onFailure: func(g cloudprovider.NodeGroup, delta int, info cloudprovider.InstanceErrorInfo, timestamp time.Time) {
					notifications++
					assert.Equal(t, group, g)
					assert.Equal(t, 1, delta)
					assert.Equal(t, wantError, info)
					assert.Equal(t, now, timestamp)
					// Fail without deadlocking the test if notification dispatch still holds the state lock.
					if !assert.True(t, csr.TryLock(), "failure observers must run outside the cluster-state lock") {
						return
					}
					csr.Unlock()
					upcoming, _ := csr.GetUpcomingNodes()
					assert.Empty(t, upcoming)
					csr.RegisterScaleDown(group, "removed-node", now, now.Add(time.Minute))
				},
			})

			require.NoError(t, csr.UpdateNodes(nil, now))
			assert.Equal(t, 1, notifications)
			assert.Len(t, csr.scaleDownRequests, 1)
			assert.Equal(t, map[string][]scaleupfailures.Record{
				"ng1": {{Delta: 1, Time: now, ErrorInfo: wantError}},
			}, csr.GetScaleUpFailures())
			assert.True(t, csr.backoff.BackoffStatus(group, nodeInfo, now).IsBackedOff)
			if tc.requested == 1 {
				assert.NotContains(t, csr.scaleUpRequests, group.Id())
			} else {
				require.Contains(t, csr.scaleUpRequests, group.Id())
				assert.Equal(t, tc.requested-1, csr.scaleUpRequests[group.Id()].Increase)
			}
			if tc.timedOut {
				group.AssertNumberOfCalls(t, "DecreaseTargetSize", 1)
			} else {
				group.AssertNotCalled(t, "DecreaseTargetSize", mock.Anything)
			}

			require.NoError(t, csr.UpdateNodes(nil, now.Add(time.Second)))
			assert.Equal(t, 1, notifications, "the same failure must not be notified twice")
			group.AssertExpectations(t)
		})
	}
}

func TestScaleUpFailureNotificationConcurrentScaleDown(t *testing.T) {
	for _, timedOut := range []bool{true, false} {
		t.Run(fmt.Sprintf("timedOut=%t", timedOut), func(t *testing.T) {
			now := time.Now()
			notifier := nodegroupchange.NewNodeGroupChangeObserversList()
			notifierLocked := make(chan struct{})
			resumeScaleDown := make(chan struct{})
			// Run before the real CSR observer, while the dispatcher holds its mutex.
			notifier.Register(&failureNotificationObserver{onScaleDown: func() {
				close(notifierLocked)
				<-resumeScaleDown
			}})
			csr, group, nodeInfo, _ := newFailureNotificationTestRegistry(t, now, timedOut, nil, 1, notifier)
			b := &coordinatedBackoff{
				backoffDelegate: csr.backoff,
				updateLocked:    make(chan struct{}),
				resumeUpdate:    make(chan struct{}),
			}
			csr.backoff = b
			resumeUpdate := sync.OnceFunc(func() { close(b.resumeUpdate) })
			releaseScaleDown := sync.OnceFunc(func() { close(resumeScaleDown) })
			t.Cleanup(resumeUpdate)
			t.Cleanup(releaseScaleDown)

			updateDone := make(chan error, 1)
			go func() { updateDone <- csr.UpdateNodes(nil, now) }()
			select {
			case <-b.updateLocked:
			case err := <-updateDone:
				t.Fatalf("update returned before reaching the state-lock barrier: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("update did not reach the state-lock barrier")
			}
			scaleDownDone := make(chan struct{})
			go func() {
				defer close(scaleDownDone)
				notifier.RegisterScaleDown(group, "removed-node", now, now.Add(time.Minute))
			}()
			select {
			case <-notifierLocked:
			case <-scaleDownDone:
				t.Fatal("scale-down notification returned before reaching the notifier-lock barrier")
			case <-time.After(5 * time.Second):
				t.Fatal("scale-down notification did not reach the notifier-lock barrier")
			}

			// Each goroutine owns one of the locks involved in the inversion before either continues.
			resumeUpdate()
			releaseScaleDown()
			select {
			case err := <-updateDone:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("cluster-state update deadlocked with a scale-down notification")
			}
			select {
			case <-scaleDownDone:
			case <-time.After(5 * time.Second):
				t.Fatal("scale-down notification did not complete")
			}
			assert.Len(t, csr.scaleDownRequests, 1)
			assert.Len(t, csr.GetScaleUpFailures()[group.Id()], 1)
			assert.True(t, b.BackoffStatus(group, nodeInfo, now).IsBackedOff)
			group.AssertExpectations(t)
		})
	}
}

func newFailureNotificationTestRegistry(t *testing.T, now time.Time, timedOut bool, revertError error, requested int, notifier *nodegroupchange.NodeGroupChangeObserversList) (*ClusterStateRegistry, *mockprovider.NodeGroup, *framework.NodeInfo, cloudprovider.InstanceErrorInfo) {
	t.Helper()
	provider := testprovider.NewTestCloudProviderBuilder().Build()
	group := &mockprovider.NodeGroup{}
	group.On("Id").Return("ng1")
	group.On("Autoprovisioned").Return(false)
	group.On("TargetSize").Return(requested, nil)
	group.On("GetOptions", mock.Anything).Return(&config.NodeGroupAutoscalingOptions{MaxNodeProvisionTime: 2 * time.Minute}, nil)
	nodeInfo := framework.NewTestNodeInfo(BuildTestNode("template", 1000, 1000))
	start := now
	wantError := cloudprovider.InstanceErrorInfo{
		ErrorClass:   cloudprovider.OutOfResourcesErrorClass,
		ErrorCode:    "RESOURCE_POOL_EXHAUSTED",
		ErrorMessage: "capacity unavailable",
	}
	var instances []cloudprovider.Instance
	if timedOut {
		start = now.Add(-3 * time.Minute)
		wantError = cloudprovider.InstanceErrorInfo{
			ErrorClass:   cloudprovider.OtherErrorClass,
			ErrorCode:    string(metrics.Timeout),
			ErrorMessage: "Scale-up timed out for node group ng1 after 3m0s",
		}
		group.On("DecreaseTargetSize", -1).Return(revertError).Once()
	} else {
		instances = []cloudprovider.Instance{{Id: "failed-instance", Status: &cloudprovider.InstanceStatus{
			State: cloudprovider.InstanceCreating, ErrorInfo: &wantError,
		}}}
	}
	group.On("Nodes").Return(instances, nil)
	provider.InsertNodeGroup(group)
	logRecorder, err := utils.NewStatusMapRecorder(&fake.Clientset{}, "kube-system", kube_record.NewFakeRecorder(5), false, "status")
	require.NoError(t, err)
	if notifier == nil {
		notifier = nodegroupchange.NewNodeGroupChangeObserversList()
	}
	csr := NewNotifiedClusterStateRegistry(provider, logRecorder, newBackoff(),
		nodegroupconfig.NewDefaultNodeGroupConfigProcessor(config.NodeGroupAutoscalingOptions{MaxNodeProvisionTime: 2 * time.Minute}),
		newMockTemplateNodeInfoRegistry(map[string]*framework.NodeInfo{"ng1": nodeInfo}),
		WithScaleStateNotifier(notifier))
	csr.RegisterScaleUp(group, requested, start)
	return csr, group, nodeInfo, wantError
}
