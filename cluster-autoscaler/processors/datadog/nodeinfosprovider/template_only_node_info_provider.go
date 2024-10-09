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

package nodeinfosprovider

import (
	"math/rand"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	"k8s.io/autoscaler/cluster-autoscaler/context"
	"k8s.io/autoscaler/cluster-autoscaler/core/utils"
	"k8s.io/autoscaler/cluster-autoscaler/metrics"
	"k8s.io/autoscaler/cluster-autoscaler/processors/datadog/common"
	"k8s.io/autoscaler/cluster-autoscaler/simulator/predicatechecker"
	"k8s.io/autoscaler/cluster-autoscaler/utils/daemonset"
	"k8s.io/autoscaler/cluster-autoscaler/utils/errors"
	"k8s.io/autoscaler/cluster-autoscaler/utils/labels"
	"k8s.io/autoscaler/cluster-autoscaler/utils/taints"
	klog "k8s.io/klog/v2"
	schedulerframework "k8s.io/kubernetes/pkg/scheduler/framework"
)

const (
	templateOnlyFuncLabel metrics.FunctionLabel = "TemplateOnlyNodeInfoProvider"
)

type nodeInfoCacheEntry struct {
	nodeInfo    *schedulerframework.NodeInfo
	lastRefresh time.Time
}

// TemplateOnlyNodeInfoProvider return NodeInfos built from node group templates.
type TemplateOnlyNodeInfoProvider struct {
	sync.Mutex
	nodeInfoCache   map[string]*nodeInfoCacheEntry
	ttl             time.Duration
	cloudProvider   cloudprovider.CloudProvider
	interrupt       chan struct{}
	forceDaemonSets bool
}

// Process returns nodeInfos built from node groups (ASGs, MIGs, VMSS) templates only, not real-world nodes.
// Process(ctx *context.AutoscalingContext, nodes []*apiv1.Node, daemonsets []*appsv1.DaemonSet, taintConfig taints.TaintConfig, currentTime time.Time) (map[string]*schedulerframework.NodeInfo, errors.AutoscalerError)
func (p *TemplateOnlyNodeInfoProvider) Process(ctx *context.AutoscalingContext, nodes []*apiv1.Node, daemonsets []*appsv1.DaemonSet, taintConfig taints.TaintConfig, currentTime time.Time) (map[string]*schedulerframework.NodeInfo, errors.AutoscalerError) {
	defer metrics.UpdateDurationFromStart(templateOnlyFuncLabel, time.Now())
	p.init(ctx.CloudProvider)

	p.Lock()
	defer p.Unlock()

	result := make(map[string]*schedulerframework.NodeInfo)
	for _, nodeGroup := range p.cloudProvider.NodeGroups() {
		var err error
		var nodeInfo *schedulerframework.NodeInfo

		id := nodeGroup.Id()
		if cacheEntry, found := p.nodeInfoCache[id]; found {
			nodeInfo, err = GetFullNodeInfoFromBase(id, cacheEntry.nodeInfo, daemonsets, ctx.PredicateChecker, taintConfig, p.forceDaemonSets)
		} else {
			// new nodegroup: this can be slow (locked) but allows discovering new nodegroups faster
			klog.V(4).Infof("No cached base NodeInfo for %s yet", id)
			nodeInfo, err = utils.GetNodeInfoFromTemplate(nodeGroup, daemonsets, taintConfig)
			if common.NodeHasLocalData(nodeInfo.Node()) {
				common.SetNodeLocalDataResource(nodeInfo)
			}
		}
		if err != nil {
			klog.Warningf("Failed to build NodeInfo template for %s: %v", id, err)
			continue
		}

		labels.UpdateDeprecatedLabels(nodeInfo.Node().ObjectMeta.Labels)
		result[id] = nodeInfo
	}

	return result, nil
}

// init starts a background refresh loop (and a shutdown channel).
// we unfortunately can't do or call that from NewTemplateOnlyNodeInfoProvider(),
// because don't have cloudProvider yet at New time.
func (p *TemplateOnlyNodeInfoProvider) init(cloudProvider cloudprovider.CloudProvider) {
	if p.interrupt != nil {
		return
	}

	p.interrupt = make(chan struct{})
	p.cloudProvider = cloudProvider
	p.refresh()
	go wait.Until(func() {
		p.refresh()
	}, 10*time.Second, p.interrupt)
}

func (p *TemplateOnlyNodeInfoProvider) refresh() {
	result := make(map[string]*nodeInfoCacheEntry)

	for _, nodeGroup := range p.cloudProvider.NodeGroups() {
		id := nodeGroup.Id()

		splay := rand.New(rand.NewSource(time.Now().UnixNano())).Intn(int(p.ttl.Seconds() + 1))
		lastRefresh := time.Now().Add(-time.Second * time.Duration(splay))

		if ng, ok := p.nodeInfoCache[id]; ok {
			if ng.lastRefresh.Add(p.ttl).After(time.Now()) {
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

// GetFullNodeInfoFromBase returns a new NodeInfo object built from provided base TemplateNodeInfo
// differs from utils.GetNodeInfoFromTemplate() in that it takes a nodeInfo as arg instead of a
// nodegroup, and doesn't need to call nodeGroup.TemplateNodeInfo() -> we can reuse a cached nodeInfo.
func GetFullNodeInfoFromBase(nodeGroupId string, baseNodeInfo *schedulerframework.NodeInfo, daemonsets []*appsv1.DaemonSet, predicateChecker predicatechecker.PredicateChecker, taintConfig taints.TaintConfig, forceDaemonSets bool) (*schedulerframework.NodeInfo, errors.AutoscalerError) {
	var pods []*apiv1.Pod
	if forceDaemonSets {
		dsPods, err := daemonset.GetDaemonSetPodsForNode(baseNodeInfo, daemonsets)
		if err != nil {
			return nil, errors.ToAutoscalerError(errors.InternalError, err)
		}
		pods = append(pods, dsPods...)
	}
	for _, podInfo := range baseNodeInfo.Pods {
		pods = append(pods, podInfo.Pod)
	}
	sanitizedNode, autoscalerErr := utils.SanitizeNode(baseNodeInfo.Node(), nodeGroupId, taintConfig)
	if autoscalerErr != nil {
		return nil, autoscalerErr
	}

	sanitizedNodeInfo := schedulerframework.NewNodeInfo(utils.SanitizePods(pods, sanitizedNode)...)
	sanitizedNodeInfo.SetNode(sanitizedNode)
	return sanitizedNodeInfo, nil
}

// CleanUp cleans up processor's internal structures.
func (p *TemplateOnlyNodeInfoProvider) CleanUp() {
	close(p.interrupt)
}

// NewTemplateOnlyNodeInfoProvider returns a NodeInfoProcessor generating NodeInfos from node group templates.
func NewTemplateOnlyNodeInfoProvider(t *time.Duration, forceDaemonSets bool) *TemplateOnlyNodeInfoProvider {
	return &TemplateOnlyNodeInfoProvider{
		ttl:             *t,
		nodeInfoCache:   make(map[string]*nodeInfoCacheEntry),
		forceDaemonSets: forceDaemonSets,
	}
}
