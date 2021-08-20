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
	"fmt"
	"math/rand"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	"k8s.io/autoscaler/cluster-autoscaler/core/utils"
	"k8s.io/autoscaler/cluster-autoscaler/processors/datadog/common"
	"k8s.io/autoscaler/cluster-autoscaler/simulator"
	"k8s.io/autoscaler/cluster-autoscaler/utils/daemonset"
	"k8s.io/autoscaler/cluster-autoscaler/utils/errors"
	kube_util "k8s.io/autoscaler/cluster-autoscaler/utils/kubernetes"
	"k8s.io/autoscaler/cluster-autoscaler/utils/labels"
	"k8s.io/autoscaler/cluster-autoscaler/utils/taints"
	klog "k8s.io/klog/v2"
	schedulerframework "k8s.io/kubernetes/pkg/scheduler/framework"
)

const nodeInfoRefreshInterval = 5 * time.Minute

type nodeInfoCacheEntry struct {
	nodeInfo    *schedulerframework.NodeInfo
	lastRefresh time.Time
}

// TemplateOnlyNodeInfoProvider return NodeInfos built from node group templates.
type TemplateOnlyNodeInfoProvider struct {
	sync.Mutex
	nodeInfoCache map[string]*nodeInfoCacheEntry
	cloudProvider cloudprovider.CloudProvider
	interrupt     chan struct{}
}

// GetNodeInfosForGroups returns nodeInfos built from node groups (ASGs, MIGs, VMSS) templates only, not real-world nodes.
// Function signature is meant to match that of std core/utils.go:GetNodeInfosForGroups
// (even though we don't use some of the args) to ease replacement and subsequent rebases, and keep untouched core tests working.
func (p *TemplateOnlyNodeInfoProvider) GetNodeInfosForGroups(nodes []*apiv1.Node, nodeInfoCache map[string]*schedulerframework.NodeInfo, cloudProvider cloudprovider.CloudProvider, listers kube_util.ListerRegistry, daemonsets []*appsv1.DaemonSet, predicateChecker simulator.PredicateChecker, ignoredTaints taints.TaintKeySet) (map[string]*schedulerframework.NodeInfo, errors.AutoscalerError) {
	start := time.Now()

	if p.interrupt == nil {
		p.interrupt = make(chan struct{})
		p.cloudProvider = cloudProvider
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
			nodeInfo, err = GetFullNodeInfoFromBase(id, cacheEntry.nodeInfo, daemonsets, predicateChecker, ignoredTaints)
		} else {
			// new nodegroup: this can be slow (locked) but allows faster discovery
			klog.V(4).Infof("No cached base NodeInfo for %s yet", id)
			nodeInfo, err = utils.GetNodeInfoFromTemplate(nodeGroup, daemonsets, predicateChecker, ignoredTaints)
		}
		if err != nil {
			klog.Warningf("Failed to build NodeInfo template for %s: %v", id, err)
			continue
		}

		labels.UpdateDeprecatedLabels(nodeInfo.Node().ObjectMeta.Labels)
		result[id] = nodeInfo
	}

	klog.V(4).Infof("TemplateOnlyNodeInfoProvider took %s for %d NodeInfos", time.Since(start), len(result))

	return result, nil
}

func (p *TemplateOnlyNodeInfoProvider) refresh() {
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

// GetFullNodeInfoFromBase returns a new NodeInfo object built from provided base TemplateNodeInfo
func GetFullNodeInfoFromBase(nodeGroupId string, baseNodeInfo *schedulerframework.NodeInfo, daemonsets []*appsv1.DaemonSet, predicateChecker simulator.PredicateChecker, ignoredTaints taints.TaintKeySet) (*schedulerframework.NodeInfo, errors.AutoscalerError) {
	pods, err := daemonset.GetDaemonSetPodsForNode(baseNodeInfo, daemonsets, predicateChecker)
	if err != nil {
		return nil, errors.ToAutoscalerError(errors.InternalError, err)
	}
	for _, podInfo := range baseNodeInfo.Pods {
		pods = append(pods, podInfo.Pod)
	}
	fullNodeInfo := schedulerframework.NewNodeInfo(pods...)
	fullNodeInfo.SetNode(baseNodeInfo.Node())
	sanitizedNodeInfo, typedErr := sanitizeNodeInfo(fullNodeInfo, nodeGroupId, ignoredTaints)
	if typedErr != nil {
		return nil, typedErr
	}
	return sanitizedNodeInfo, nil
}

// copied from core/utils/
func sanitizeNodeInfo(nodeInfo *schedulerframework.NodeInfo, nodeGroupName string, ignoredTaints taints.TaintKeySet) (*schedulerframework.NodeInfo, errors.AutoscalerError) {
	// Sanitize node name.
	sanitizedNode, err := sanitizeTemplateNode(nodeInfo.Node(), nodeGroupName, ignoredTaints)
	if err != nil {
		return nil, err
	}

	// Update nodename in pods.
	sanitizedPods := make([]*apiv1.Pod, 0)
	for _, podInfo := range nodeInfo.Pods {
		sanitizedPod := podInfo.Pod.DeepCopy()
		sanitizedPod.Spec.NodeName = sanitizedNode.Name
		sanitizedPods = append(sanitizedPods, sanitizedPod)
	}

	// Build a new node info.
	sanitizedNodeInfo := schedulerframework.NewNodeInfo(sanitizedPods...)
	sanitizedNodeInfo.SetNode(sanitizedNode)
	return sanitizedNodeInfo, nil
}

// copied from core/utils/
func sanitizeTemplateNode(node *apiv1.Node, nodeGroup string, ignoredTaints taints.TaintKeySet) (*apiv1.Node, errors.AutoscalerError) {
	newNode := node.DeepCopy()
	nodeName := fmt.Sprintf("template-node-for-%s-%d", nodeGroup, rand.Int63())
	newNode.Labels = make(map[string]string, len(node.Labels))
	for k, v := range node.Labels {
		if k != apiv1.LabelHostname {
			newNode.Labels[k] = v
		} else {
			newNode.Labels[k] = nodeName
		}
	}
	newNode.Name = nodeName
	newNode.Spec.Taints = taints.SanitizeTaints(newNode.Spec.Taints, ignoredTaints)
	return newNode, nil
}

// CleanUp cleans up processor's internal structures.
func (p *TemplateOnlyNodeInfoProvider) CleanUp() {
	close(p.interrupt)
}

// NewTemplateOnlyNodeInfoProvider returns a NodeInfoProcessor generating NodeInfos from node group templates.
func NewTemplateOnlyNodeInfoProvider() *TemplateOnlyNodeInfoProvider {
	return &TemplateOnlyNodeInfoProvider{
		nodeInfoCache: make(map[string]*nodeInfoCacheEntry),
	}
}
