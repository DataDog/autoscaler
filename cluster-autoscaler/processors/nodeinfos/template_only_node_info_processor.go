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

package nodeinfos

import (
	"math/rand"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	"k8s.io/autoscaler/cluster-autoscaler/context"
	"k8s.io/autoscaler/cluster-autoscaler/core/utils"
	"k8s.io/autoscaler/cluster-autoscaler/processors/datadog/common"
	"k8s.io/autoscaler/cluster-autoscaler/utils/taints"
	klog "k8s.io/klog/v2"
	schedulerframework "k8s.io/kubernetes/pkg/scheduler/framework/v1alpha1"
)

const nodeInfoRefreshInterval = 5 * time.Minute

type nodeInfoCacheEntry struct {
	nodeInfo    *schedulerframework.NodeInfo
	lastRefresh time.Time
}

// TemplateOnlyNodeInfoProcessor return NodeInfos built from node group templates.
type TemplateOnlyNodeInfoProcessor struct {
	sync.Mutex
	nodeInfoCache map[string]*nodeInfoCacheEntry
	cloudProvider cloudprovider.CloudProvider
	interrupt     chan struct{}
}

// Process returns nodeInfos built from node groups templates.
func (p *TemplateOnlyNodeInfoProcessor) Process(ctx *context.AutoscalingContext, nodeInfosForNodeGroups map[string]*schedulerframework.NodeInfo, daemonsets []*appsv1.DaemonSet, ignoredTaints taints.TaintKeySet) (map[string]*schedulerframework.NodeInfo, error) {
	start := time.Now()

	if p.interrupt == nil {
		p.interrupt = make(chan struct{})
		p.cloudProvider = ctx.CloudProvider
		p.refresh()
		go wait.Until(func() {
			p.refresh()
		}, 10*time.Second, p.interrupt)
	}

	p.Lock()
	defer p.Unlock()

	result := make(map[string]*schedulerframework.NodeInfo)
	for _, nodeGroup := range p.cloudProvider.NodeGroups() {
		var err error
		var nodeInfo *schedulerframework.NodeInfo

		id := nodeGroup.Id()
		if cacheEntry, found := p.nodeInfoCache[id]; found {
			nodeInfo, err = utils.GetFullNodeInfoFromBase(id, cacheEntry.nodeInfo, daemonsets, ctx.PredicateChecker, ignoredTaints)
		} else {
			// new nodegroup: this can be slow (locked) but allows faster discovery
			klog.V(4).Infof("No cached base NodeInfo for %s yet", id)
			nodeInfo, err = utils.GetNodeInfoFromTemplate(nodeGroup, daemonsets, ctx.PredicateChecker, ignoredTaints)
		}
		if err != nil {
			klog.Warningf("Failed to build NodeInfo template for %s: %v", id, err)
			continue
		}
		result[id] = nodeInfo
	}

	klog.V(4).Infof("TemplateOnlyNodeInfoProcessor took %s for %d NodeInfos", time.Since(start), len(result))

	return result, nil
}

func (p *TemplateOnlyNodeInfoProcessor) refresh() {
	result := make(map[string]*nodeInfoCacheEntry)

	for _, nodeGroup := range p.cloudProvider.NodeGroups() {
		id := nodeGroup.Id()

		splay := rand.New(rand.NewSource(time.Now().UnixNano())).Intn(int(nodeInfoRefreshInterval.Seconds() + 1))
		lastRefresh := time.Now().Add(-time.Second * time.Duration(splay))

		if ng, ok := p.nodeInfoCache[id]; ok {
			if ng.lastRefresh.Add(nodeInfoRefreshInterval).After(time.Now()) {
				result[id] = ng
				continue
			}
			lastRefresh = time.Now()
		}

		nodeInfo, err := nodeGroup.TemplateNodeInfo()
		if err != nil {
			klog.Warningf("Unable to build template node for %s: %v", id, err)
			continue
		}

		// Virtual nodes in NodeInfo templates (built from ASG / MIGS / VMSS) having the
		// local-storage:true label now also gets the Datadog local-storage custom resource
		if common.NodeHasLocalData(nodeInfo.Node()) {
			common.SetNodeLocalDataResource(nodeInfo)
		}

		result[id] = &nodeInfoCacheEntry{
			nodeInfo:    nodeInfo,
			lastRefresh: lastRefresh,
		}
	}

	p.Lock()
	p.nodeInfoCache = result
	p.Unlock()
}

// CleanUp cleans up processor's internal structures.
func (p *TemplateOnlyNodeInfoProcessor) CleanUp() {
	close(p.interrupt)
}

// NewTemplateOnlyNodeInfoProcessor returns a NodeInfoProcessor generating NodeInfos from node group templates.
func NewTemplateOnlyNodeInfoProcessor() *TemplateOnlyNodeInfoProcessor {
	return &TemplateOnlyNodeInfoProcessor{
		nodeInfoCache: make(map[string]*nodeInfoCacheEntry),
	}
}
