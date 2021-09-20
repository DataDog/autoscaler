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
	"k8s.io/autoscaler/cluster-autoscaler/core"
	"k8s.io/autoscaler/cluster-autoscaler/metrics"
	"k8s.io/autoscaler/cluster-autoscaler/processors/datadog/common"
	"k8s.io/autoscaler/cluster-autoscaler/processors/datadog/nodeinfosprovider/podtemplate"
	"k8s.io/autoscaler/cluster-autoscaler/simulator"
	schedulerframework "k8s.io/autoscaler/cluster-autoscaler/simulator/framework"
	"k8s.io/autoscaler/cluster-autoscaler/utils/errors"
	"k8s.io/autoscaler/cluster-autoscaler/utils/labels"
	"k8s.io/autoscaler/cluster-autoscaler/utils/taints"
	klog "k8s.io/klog/v2"
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

	podTemplateProcessor podtemplate.Interface
}

// Process returns nodeInfos built from node groups (ASGs, MIGs, VMSS) templates only, not real-world nodes.
// Reason for using this instead of upstream's MixedTemplateNodeInfoProvider at Datadog are:
// * On upstream (using real nodes once they show up) "upscale from zero" and balance-similar don't work together
// * We have to alter nodes in order to support accounting for local-data volumes
// A downside of building nodeInfos from templates (nodegroups sppecs) only is that it's more costly than
// using real nodes, which is why we're doing it asynchronously.
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
			nodeInfo, err = p.SanitizedTemplateNodeInfoFromNodeGroupCached(id, cacheEntry.nodeInfo, daemonsets, taintConfig)
			if err != nil {
				klog.Warningf("Failed to obtain NodeInfo template from cache for %s: %v", id, err)
				continue
			}
		} else {
			// new nodegroup: this can be slow (locked) but allows discovering new nodegroups faster
			klog.V(4).Infof("No cached base NodeInfo for %s yet", id)
			nodeInfo, err = simulator.SanitizedTemplateNodeInfoFromNodeGroup(nodeGroup, daemonsets, taintConfig)
			if err != nil {
				klog.Warningf("Failed to build NodeInfo template for %s: %v", id, err)
				continue
			}
			if common.NodeHasLocalData(nodeInfo.Node()) {
				common.SetNodeLocalDataResource(nodeInfo)
			}
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

// SanitizedTemplateNodeInfoFromNodeGroupCached is a copy of simulator.SanitizedTemplateNodeInfoFromNodeGroup,
// but using a provided nodeInfo rather than calling TemplateNodeInfo() (which is costly) + injecting
// the datadog-agent pod(s) inferred from a podTemplate (== the agents pods that aren't managed by DaemonSets).
func (p *TemplateOnlyNodeInfoProvider) SanitizedTemplateNodeInfoFromNodeGroupCached(id string, baseNodeInfo *schedulerframework.NodeInfo,
	daemonsets []*appsv1.DaemonSet, taintConfig taints.TaintConfig) (*schedulerframework.NodeInfo, errors.AutoscalerError) {
	labels.UpdateDeprecatedLabels(baseNodeInfo.Node().ObjectMeta.Labels)

	sim, err := simulator.SanitizedTemplateNodeInfoFromNodeInfo(baseNodeInfo, id, daemonsets, true, taintConfig)
	if err != nil {
		return sim, err
	}

	// this is only meant to support main agent EDS nowadays (whose resources usage are discovered from a podTemplate)
	podTpls, err2 := p.podTemplateProcessor.GetDaemonSetPodsFromPodTemplateForNode(sim, taintConfig)
	if err2 != nil {
		return nil, errors.ToAutoscalerError(errors.InternalError, err2)
	}
	for _, pod := range podTpls {
		sim.AddPod(&schedulerframework.PodInfo{Pod: pod})
	}

	return sim, nil
}

// CleanUp cleans up processor's internal structures.
func (p *TemplateOnlyNodeInfoProvider) CleanUp() {
	p.podTemplateProcessor.CleanUp()
	close(p.interrupt)
}

// NewTemplateOnlyNodeInfoProvider returns a NodeInfoProcessor generating NodeInfos from node group templates.
func NewTemplateOnlyNodeInfoProvider(t *time.Duration, forceDaemonSets bool, opts *core.AutoscalerOptions) *TemplateOnlyNodeInfoProvider {
	return &TemplateOnlyNodeInfoProvider{
		ttl:                  *t,
		nodeInfoCache:        make(map[string]*nodeInfoCacheEntry),
		forceDaemonSets:      forceDaemonSets,
		podTemplateProcessor: podtemplate.NewPodTemplateProcessor(opts),
	}
}
