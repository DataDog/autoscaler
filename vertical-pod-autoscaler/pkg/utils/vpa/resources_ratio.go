/*
Copyright 2018 The Kubernetes Authors.

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

package api

import (
	"fmt"
	"github.com/dominikbraun/graph"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	"k8s.io/klog/v2"
)

// NewResourceRatioRecommendationProcessor constructs new RecommendationsProcessor that adjusts recommendation
// for given pod to obey VPA resources maintainedRatio policy
func NewResourceRatioRecommendationProcessor() RecommendationProcessor {
	return &resourceRatioRecommendationProcessor{}
}

type resourceRatioRecommendationProcessor struct {
}

// resourceRatioRecommendationProcessor must implement RecommendationProcessor
var _ RecommendationProcessor = &resourceRatioRecommendationProcessor{}

// Apply returns a recommendation for the given pod, adjusted to obey maintainedRatio policy
func (r resourceRatioRecommendationProcessor) Apply(
	podRecommendation *vpa_types.RecommendedPodResources,
	policy *vpa_types.PodResourcePolicy,
	conditions []vpa_types.VerticalPodAutoscalerCondition,
	pod *apiv1.Pod) (*vpa_types.RecommendedPodResources, ContainerToAnnotationsMap, error) {

	if podRecommendation == nil && policy == nil {
		// If there is no recommendation and no policies have been defined then no recommendation can be computed.
		return nil, nil, nil
	}
	if podRecommendation == nil {
		// Policies have been specified. Create an empty recommendation so that the policies can be applied correctly.
		podRecommendation = new(vpa_types.RecommendedPodResources)
	}
	updatedRecommendations := []vpa_types.RecommendedContainerResources{}
	containerToAnnotationsMap := ContainerToAnnotationsMap{}

	for _, containerRecommendation := range podRecommendation.ContainerRecommendations {
		container := getContainer(containerRecommendation.ContainerName, pod)
		if container == nil {
			klog.V(2).Infof("no matching Container found for recommendation %s", containerRecommendation.ContainerName)
			continue
		}

		updatedContainerResources, containerAnnotations, err := getRecommendationForContainerWithRatioApplied(
			*container, &containerRecommendation, policy)

		if len(containerAnnotations) != 0 {
			containerToAnnotationsMap[containerRecommendation.ContainerName] = containerAnnotations
		}

		if err != nil {
			return nil, nil, fmt.Errorf("cannot update recommendation for container name %v", container.Name)
		}
		updatedRecommendations = append(updatedRecommendations, *updatedContainerResources)
	}

	return &vpa_types.RecommendedPodResources{ContainerRecommendations: updatedRecommendations}, containerToAnnotationsMap, nil
}

// getRecommendationForContainerWithRatioApplied returns a recommendation for the given container, adjusted to obey maintainedRatios policy
func getRecommendationForContainerWithRatioApplied(
	container apiv1.Container,
	containerRecommendation *vpa_types.RecommendedContainerResources,
	policy *vpa_types.PodResourcePolicy) (*vpa_types.RecommendedContainerResources, []string, error) {

	// containerPolicy can be nil (user does not have to configure it).
	containerPolicy := GetContainerResourcePolicy(container.Name, policy)

	amendedRecommendations := containerRecommendation.DeepCopy()
	generatedAnnotations := make([]string, 0)

	process := func(recommendation apiv1.ResourceList, genAnnotations bool) {
		annotations := applyMaintainRatioVPAPolicy(recommendation, containerPolicy, container.Resources.Requests)
		if genAnnotations {
			generatedAnnotations = append(generatedAnnotations, annotations...)
		}
	}

	process(amendedRecommendations.Target, true)
	process(amendedRecommendations.LowerBound, false)
	process(amendedRecommendations.UpperBound, false)

	return amendedRecommendations, generatedAnnotations, nil
}

// applyMaintainRatioVPAPolicy uses the maintainRatio constraints and the defined ratios in the Pod
// and amend the recommendation to respect the original ratios
func applyMaintainRatioVPAPolicy(recommendation apiv1.ResourceList, policy *vpa_types.ContainerResourcePolicy, containerOriginalResources apiv1.ResourceList) []string {
	if policy == nil || policy.MaintainedRatios == nil {
		return nil
	}

	maintainedRatiosCalculationOrdered, err := getMaintainedRatiosCalculationOrder(policy.MaintainedRatios)
	if err != nil {
		klog.V(1).ErrorS(err, "The VPA definition is not correct and should not have passed the admission. Can't apply MaintainedRatio Processor")
		return nil
	}
	annotations := make([]string, 0)

	for _, ratioConstraint := range maintainedRatiosCalculationOrdered {
		qA := containerOriginalResources.Name(ratioConstraint[0], resource.DecimalSI)
		qB := containerOriginalResources.Name(ratioConstraint[1], resource.DecimalSI)

		if qA.MilliValue() == 0 {
			// TODO annotation for ratio with null divider
			continue
		}

		rA := recommendation.Name(ratioConstraint[0], resource.DecimalSI)
		rB := recommendation.Name(ratioConstraint[1], resource.DecimalSI)
		rB.SetMilli(rA.MilliValue() * qB.MilliValue() / qA.MilliValue())
		recommendation[ratioConstraint[1]] = *rB
	}
	return annotations
}

var resourceNameHash = func(r apiv1.ResourceName) string { return string(r) }

// getMaintainedRatiosCalculationOrder validates (no cycle) and sort the constraints
// in an order that should be used to compute resource values
// for example if the user gives:
// {"B","C"},{"A","B"} , we must first compute B using value of A, and then only compute C using value of B
// this function will return:
// {"A","B"},{"B","C"}
// The function will return an error if the graph defined contains cycle.
// The function support multiple graphs like: {"A","B"},{"C","D"} ... but in that case the ordered output is undetermined
// it could be {"A","B"},{"C","D"} or {"C","D"},{"A","B"}
func getMaintainedRatiosCalculationOrder(m [][2]apiv1.ResourceName) ([][2]apiv1.ResourceName, error) {

	// Create the graph that represent the relation between resources
	// This graph is directed and must be acyclic
	g := graph.New(resourceNameHash, graph.Directed(), graph.PermitCycles())
	doneVertex := map[apiv1.ResourceName]struct{}{}
	for _, s := range m {
		if _, ok := doneVertex[s[0]]; !ok {
			g.AddVertex(s[0])
			doneVertex[s[0]] = struct{}{}
		}
		if _, ok := doneVertex[s[1]]; !ok {
			g.AddVertex(s[1])
			doneVertex[s[1]] = struct{}{}
		}
		err := g.AddEdge(resourceNameHash(s[0]), resourceNameHash(s[1]))
		if err != nil {
			return nil, err
		}
	}

	predecessorsMap, err := g.PredecessorMap()
	if err != nil {
		klog.V(1).Infof("Error with Predescessor: %v", err)
		return nil, err
	}

	ordered, err := graph.TopologicalSort(g)
	if err != nil {
		klog.V(1).Infof("Error with TopologicalSort: %v", err)
		return nil, err
	}

	// Check that no node of the graph has more than 1 predecessor
	for k, v := range predecessorsMap {
		if len(v) > 1 {
			klog.V(1).Infof("Resource '%s' has more that one predecessor for value computation", k)
			return nil, fmt.Errorf("Resource '%s' has more than one predecessor in maintainedRatios", k)
		}
	}

	orderedResult := [][2]apiv1.ResourceName{}

	// build the ordered list of relation
	// this list will tell us in which order we should compute resources
	for _, resource := range ordered {
		m := predecessorsMap[resource]
		var predecessor string
		if len(m) == 0 {
			continue
		}
		for k := range m { // we are sure that there is only one element here
			predecessor = k
		}
		orderedResult = append(orderedResult, [2]apiv1.ResourceName{apiv1.ResourceName(predecessor), apiv1.ResourceName(resource)})
	}
	return orderedResult, err
}
