package customresources

import (
	"strconv"
	"strings"

	apiv1 "k8s.io/api/core/v1"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	"k8s.io/autoscaler/cluster-autoscaler/context"
	"k8s.io/autoscaler/cluster-autoscaler/utils/errors"
	"k8s.io/autoscaler/cluster-autoscaler/utils/kubernetes"
	"k8s.io/klog/v2"
)

const datadogCustomResourceLabelPrefix = "k8s.io/cluster-autoscaler/node-template/resources/datadog/"

// DatadogCustomResourcesProcessor handles regular GPU resources and Datadog custom resources.
// It assumes, that the resources may not become allocatable immediately after the node creation.
// It uses tags to predict the type/count of resources in that case.
type DatadogCustomResourcesProcessor struct {
	gpuProcessor GpuCustomResourcesProcessor
}

// FilterOutNodesWithUnreadyResources removes nodes that should have resources, but don't have
// it in allocatable from ready nodes list and updates their status to unready on all nodes list.
// This is a hack/workaround for nodes with resources coming up without finished configuration, resulting
// in resources missing from their allocatable and capacity.
func (p *DatadogCustomResourcesProcessor) FilterOutNodesWithUnreadyResources(context *context.AutoscalingContext, allNodes, readyNodes []*apiv1.Node) ([]*apiv1.Node, []*apiv1.Node) {
	gpuPatchedAllNodes, gpuReadyNodes := p.gpuProcessor.FilterOutNodesWithUnreadyResources(context, allNodes, readyNodes)

	newAllNodes := make([]*apiv1.Node, 0)
	newReadyNodes := make([]*apiv1.Node, 0)
	nodesWithUnreadyDatadogResources := make(map[string]*apiv1.Node)
	for _, node := range gpuReadyNodes {
		isReady := true
		for customResource, _ := range getDatadogCustomResources(node) {
			datadogCustomResource := apiv1.ResourceName(customResource)
			allocatable, found := node.Status.Allocatable[datadogCustomResource]
			if !found || allocatable.IsZero() {
				klog.V(3).Infof("Overriding status of node %v, which seems to have unready custom resource %v",
					node.Name, customResource)
				isReady = false
			}
		}
		if isReady {
			newReadyNodes = append(newReadyNodes, node)
		} else {
			nodesWithUnreadyDatadogResources[node.Name] = kubernetes.GetUnreadyNodeCopy(node, kubernetes.ResourceUnready)
		}
	}
	// Override any node with unready resource with its "unready" copy
	for _, node := range gpuPatchedAllNodes {
		if newNode, found := nodesWithUnreadyDatadogResources[node.Name]; found {
			newAllNodes = append(newAllNodes, newNode)
		} else {
			newAllNodes = append(newAllNodes, node)
		}
	}
	return newAllNodes, newReadyNodes
}

// GetNodeResourceTargets returns mapping of resource names to their targets.
// This includes resources which are not yet ready to use and visible in kubernetes.
func (p *DatadogCustomResourcesProcessor) GetNodeResourceTargets(context *context.AutoscalingContext, node *apiv1.Node, nodeGroup cloudprovider.NodeGroup) ([]CustomResourceTarget, errors.AutoscalerError) {
	targets, err := p.gpuProcessor.GetNodeResourceTargets(context, node, nodeGroup)
	if err != nil {
		return targets, err
	}

	for customResource, value := range getDatadogCustomResources(node) {
		var targetValue int64 = 0

		datadogCustomResource := apiv1.ResourceName(customResource)
		allocatable, found := node.Status.Allocatable[datadogCustomResource]
		if found && !allocatable.IsZero() {
			// First try to get the value from allocatable if available on the node
			targetValue = allocatable.Value()
		} else {
			// Otherwise try to deduce the resource value from node labels
			intValue, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				klog.Errorf("Failed to parse datadog custom resource %v value %v: %v", customResource, value, err)
				return targets, errors.NewAutoscalerError(errors.InternalError, "could not parse datadog label custom resource value")
			}
			targetValue = intValue
		}
		targets = append(targets, CustomResourceTarget{
			ResourceType:  customResource,
			ResourceCount: targetValue,
		})
	}

	return targets, nil
}

// CleanUp cleans up processor's internal structures.
func (p *DatadogCustomResourcesProcessor) CleanUp() {
}

func getDatadogCustomResources(node *apiv1.Node) map[string]string {
	customResources := make(map[string]string, 0)
	if node != nil {
		for label, value := range node.Labels {
			if strings.HasPrefix(label, datadogCustomResourceLabelPrefix) {
				customResource := label[len(datadogCustomResourceLabelPrefix):]
				customResources[customResource] = value
			}
		}
	}
	return customResources
}
