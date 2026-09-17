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
	"runtime"
	"testing"
	"time"

	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	testprovider "k8s.io/autoscaler/cluster-autoscaler/cloudprovider/test"
	"k8s.io/autoscaler/cluster-autoscaler/clusterstate/scaleupfailures"
	"k8s.io/autoscaler/cluster-autoscaler/observers/nodegroupchange"
	"k8s.io/autoscaler/cluster-autoscaler/simulator/framework"
	testutils "k8s.io/autoscaler/cluster-autoscaler/utils/test"
)

type scaleDownNotificationGate struct {
	entered chan struct{}
	release chan struct{}
}

func (g *scaleDownNotificationGate) RegisterScaleUp(cloudprovider.NodeGroup, int, time.Time) {}
func (g *scaleDownNotificationGate) RegisterFailedScaleUp(cloudprovider.NodeGroup, int, cloudprovider.InstanceErrorInfo, time.Time) {
}
func (g *scaleDownNotificationGate) RegisterFailedScaleDown(cloudprovider.NodeGroup, string, time.Time) {
}
func (g *scaleDownNotificationGate) RegisterScaleDown(cloudprovider.NodeGroup, string, time.Time, time.Time) {
	close(g.entered)
	<-g.release
}

// TestScaleFailureNotificationDoesNotBlockOnScaleDown exercises real notifier and CSR callbacks.
// Holding the CSR lock models the critical section in updateClusterStateRegistry.
func TestScaleFailureNotificationDoesNotBlockOnScaleDown(t *testing.T) {
	provider := testprovider.NewTestCloudProviderBuilder().Build()
	provider.AddNodeGroup("ng", 0, 10, 1)
	ng := provider.GetNodeGroup("ng")
	csr := &ClusterStateRegistry{
		scaleUpFailures: scaleupfailures.NewRegistry(),
		templateNodeInfoRegistry: newMockTemplateNodeInfoRegistry(map[string]*framework.NodeInfo{
			"ng": framework.NewNodeInfo(testutils.BuildTestNode("template", 1000, 1000), nil),
		}),
		backoff: newBackoff(),
	}
	notifier := nodegroupchange.NewNodeGroupChangeObserversList()
	gate := &scaleDownNotificationGate{entered: make(chan struct{}), release: make(chan struct{})}
	notifier.Register(gate)
	notifier.Register(csr)
	csr.Lock()
	downDone := make(chan struct{})
	go func() { notifier.RegisterScaleDown(ng, "node", time.Now(), time.Now()); close(downDone) }()
	// On the old implementation, scale-down now holds the shared notifier mutex.
	<-gate.entered
	// Let its CSR callback wait for the registry lock while failure dispatch runs.
	close(gate.release)
	failedDone := make(chan struct{})
	go func() {
		notifier.RegisterFailedScaleUp(ng, 1, cloudprovider.InstanceErrorInfo{ErrorCode: "provisioning-state-failed"}, time.Now())
		close(failedDone)
	}()
	select {
	case <-failedDone:
		csr.Unlock()
	case <-time.After(time.Second):
		b := make([]byte, 1<<20)
		n := runtime.Stack(b, true)
		t.Errorf("scale-failure notification cannot complete while CSR lock is held: lock inversion\n%s", b[:n])
		csr.Unlock()
		<-failedDone
	}
	<-downDone
	if got := len(csr.GetScaleUpFailures()["ng"]); got != 1 {
		t.Fatalf("failure records = %d, want 1", got)
	}
	if got := len(csr.scaleDownRequests); got != 1 {
		t.Fatalf("scale-down records = %d, want 1", got)
	}
}
