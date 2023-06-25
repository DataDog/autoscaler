/*
Copyright 2022 The Kubernetes Authors.

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

package routines

import (
	"encoding/json"
	"fmt"

	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"

	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	controllerfetcher "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/input/controller_fetcher"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
)

const (
	vpaPostProcessorResourceRationSuffix = "_resourceRatios"
)

// ResourceRatioRecommendationPostProcessor ensures that defined ratio constraints between resources is applied.
// The definition is done via annotation on the VPA object with format: vpa-post-processor.kubernetes.io/{containerName}_resourceRatios={"A":{"resource":"B","ratio":"1.8"}}
// The value of the annotation is map of resource to {resource,ratio}. If the value is {"cpu":{"resource":"memory"}} that means that the CPU recommendation is used and the memory recommendation is computed to match the initial ratio CPU/Memory that is defined in the podTemplateSpec.
// If instead of the ratio defined in the podTemplate, you want to use a specific value, just pass it {"cpu":{"resource":"memory","ratio":15000000000}} , this is 15G of Mem per Core.
type ResourceRatioRecommendationPostProcessor struct {
	ControllerFetcher controllerfetcher.ControllerFetcher
}

// Process applies the Resource Ratio post-processing to the recommendation.
func (r *ResourceRatioRecommendationPostProcessor) Process(vpa *model.Vpa, recommendation *vpa_types.RecommendedPodResources, _ *vpa_types.PodResourcePolicy) *vpa_types.RecommendedPodResources {
	ratios := readResourceRatioFromVPAAnnotations(vpa)
	if len(ratios) == 0 || recommendation == nil {
		return recommendation
	}
	klog.Infof("Using ratio post-processor vpa=%", vpa.ID.VpaName)
	podTemplate, err := r.ControllerFetcher.GetPodTemplateFromTopMostWellKnown(&controllerfetcher.ControllerKeyWithAPIVersion{
		ControllerKey: controllerfetcher.ControllerKey{
			Namespace: vpa.ID.Namespace,
			Kind:      vpa.TargetRef.Kind,
			Name:      vpa.TargetRef.Name,
		},
		ApiVersion: vpa.TargetRef.APIVersion,
	})
	if err != nil {
		klog.Errorf("Failed to apply ResourceRatioRecommendationPostProcessor (controller fetch) to vpa %s/%s due to error: %#v", vpa.ID.Namespace, vpa.ID.VpaName, err)
		return recommendation
	}

	pod := newPodFromTemplate(podTemplate)

	updatedRecommendation, err := r.apply(recommendation, ratios, pod)
	if err != nil {
		klog.Errorf("Failed to apply ResourceRatioRecommendationPostProcessor to vpa %s/%s due to error: %#v", vpa.ID.Namespace, vpa.ID.VpaName, err)
	}
	return updatedRecommendation
}

type RatioDefinition struct {
	Resource string   `json:"resource"`
	Ratio    *float64 `json:"ratio"`
}

type resourceRatio struct {
	original apiv1.ResourceName
	target   apiv1.ResourceName
	ratio    *float64
}

type resourceRatioList []resourceRatio

func readResourceRatioFromVPAAnnotations(vpa *model.Vpa) map[string]resourceRatioList {
	ratios := map[string]resourceRatioList{}
	for key, value := range vpa.Annotations {
		containerName := extractContainerName(key, vpaPostProcessorPrefix, vpaPostProcessorResourceRationSuffix)
		if containerName == "" {
			continue
		}

		ratioDef := map[string]RatioDefinition{} // the key is the primary resource on whic h the ratio is applied

		if err := json.Unmarshal([]byte(value), &ratioDef); err != nil {
			klog.Errorf("Skipping ratio definition '%s' for container '%s' in vpa %s/%s due to bad format, error:%#v", value, containerName, vpa.ID.Namespace, vpa.ID.VpaName, err)
			continue
		}

		var asList resourceRatioList
		for k, v := range ratioDef {
			asList = append(asList, resourceRatio{
				original: apiv1.ResourceName(k),
				target:   apiv1.ResourceName(v.Resource),
				ratio:    v.Ratio,
			})
		}
		ratios[containerName] = asList
	}
	return ratios
}

// ResourceRatioRecommendationPostProcessor must implement RecommendationProcessor
var _ RecommendationPostProcessor = &ResourceRatioRecommendationPostProcessor{}

// Apply returns a recommendation for the given pod, adjusted to obey maintainedRatio policy
func (r *ResourceRatioRecommendationPostProcessor) apply(
	podRecommendation *vpa_types.RecommendedPodResources,
	ratios map[string]resourceRatioList,
	pod *apiv1.Pod) (*vpa_types.RecommendedPodResources, error) {

	if podRecommendation == nil {
		// If there is no recommendation let's skip that post-processor.
		return nil, nil
	}
	updatedRecommendations := []vpa_types.RecommendedContainerResources{}

	for _, containerRecommendation := range podRecommendation.ContainerRecommendations {
		container := getContainer(containerRecommendation.ContainerName, pod)
		if container == nil {
			klog.V(2).Infof("no matching Container found for recommendation %s", containerRecommendation.ContainerName)
			continue
		}
		ratiosDef := ratios[container.Name]
		klog.Infof("Using ratio post-processor on container=%s ratio=%v", container.Name, ratiosDef)
		updatedContainerResources, err := getRecommendationForContainerWithRatioApplied(*container, &containerRecommendation, ratiosDef)
		if err != nil {
			return nil, fmt.Errorf("cannot update recommendation for container name %v", container.Name)
		}
		updatedRecommendations = append(updatedRecommendations, *updatedContainerResources)
	}

	return &vpa_types.RecommendedPodResources{ContainerRecommendations: updatedRecommendations}, nil
}

func newPodFromTemplate(template *apiv1.PodTemplateSpec) *apiv1.Pod {
	return &apiv1.Pod{
		ObjectMeta: *template.ObjectMeta.DeepCopy(),
		Spec:       *template.Spec.DeepCopy(),
	}
}

func getContainer(containerName string, pod *apiv1.Pod) *apiv1.Container {
	for i, c := range pod.Spec.Containers {
		if c.Name == containerName {
			return &pod.Spec.Containers[i]
		}
	}
	for i, c := range pod.Spec.InitContainers {
		if c.Name == containerName {
			return &pod.Spec.InitContainers[i]
		}
	}
	return nil
}

// getRecommendationForContainerWithRatioApplied returns a recommendation for the given container, adjusted to obey maintainedRatios policy
func getRecommendationForContainerWithRatioApplied(
	container apiv1.Container,
	containerRecommendation *vpa_types.RecommendedContainerResources,
	containerPolicy resourceRatioList) (*vpa_types.RecommendedContainerResources, error) {

	amendedRecommendations := containerRecommendation.DeepCopy()

	process := func(recommendation apiv1.ResourceList) {
		applyMaintainRatioVPAPolicy(recommendation, containerPolicy, container.Resources.Requests)
	}

	process(amendedRecommendations.Target)
	process(amendedRecommendations.LowerBound)
	process(amendedRecommendations.UpperBound)

	return amendedRecommendations, nil
}

// applyMaintainRatioVPAPolicy uses the maintainRatio constraints and the defined ratios in the Pod
// and amend the recommendation to respect the original ratios
func applyMaintainRatioVPAPolicy(recommendation apiv1.ResourceList, ratiosPolicies resourceRatioList, containerOriginalResources apiv1.ResourceList) {
	if ratiosPolicies == nil {
		return
	}

	maintainedRatiosCalculationOrdered, err := getMaintainedRatiosCalculationOrder(ratiosPolicies)
	if err != nil {
		klog.V(1).ErrorS(err, "The VPA definition is not correct and should not have passed the admission (if ratio policies are checked). Can't apply MaintainedRatio Processor")
		return
	}

	for _, ratioConstraint := range maintainedRatiosCalculationOrdered {
		var ratio float64
		// if the ration is define explicitely, use the defined value, else use the original ratio of the podSpec
		if ratioConstraint.ratio != nil {
			ratio = *ratioConstraint.ratio
		} else {
			qA := containerOriginalResources.Name(ratioConstraint.original, resource.DecimalSI)
			qB := containerOriginalResources.Name(ratioConstraint.target, resource.DecimalSI)

			if qA.MilliValue() == 0 {
				continue
			}
			ratio = float64(qB.MilliValue()) / float64(qA.MilliValue())
		}
		klog.Infof("%s=>%s using %v", ratioConstraint.original, ratioConstraint.target, ratio)
		rA := recommendation.Name(ratioConstraint.original, resource.DecimalSI)
		rB := recommendation.Name(ratioConstraint.target, resource.DecimalSI)
		rB.SetMilli(int64(float64(rA.MilliValue()) * ratio))
		recommendation[ratioConstraint.target] = *rB
	}
	return
}

// getMaintainedRatiosCalculationOrder validates (no cycle) and sort the constraints
// in an order that should be used to compute resource values
// for example if the user gives:
// {"B":"C"},{"A":"B"} , we must first compute B using value of A, and then only compute C using value of B
// this function will return:
// {"A":"B"},{"B":"C"}
// The function will return an error if the graph defined contains cycle.
// The function support multiple graphs like: {"A":"B"},{"C":"D"} ... but in that case the ordered output is undetermined
// it could be {"A":"B"},{"C":"D"} or {"C":"D"},{"A":"B"}
func getMaintainedRatiosCalculationOrder(ratios resourceRatioList) (resourceRatioList, error) {
	ordered, predecessorsMap, ok := getSortedResourceAndPredecessors(ratios)
	if !ok {
		klog.V(1).Infof("Error the graph is not acyclic")
		return nil, fmt.Errorf("Error the graph is not acyclic")
	}

	// Check that no resourceNode of the graph has more than 1 predecessor
	for k, v := range predecessorsMap {
		if len(v) > 1 {
			klog.V(1).Infof("Resource '%s' has more that one predecessor for value computation", k)
			return nil, fmt.Errorf("Resource '%s' has more than one predecessor in maintainedRatios", k)
		}
	}
	indexedRatio := map[apiv1.ResourceName]resourceRatio{}
	for _, rr := range ratios {
		indexedRatio[rr.original+"-"+rr.target] = rr
	}

	orderedResult := resourceRatioList{}

	// build the ordered list of relation
	// this list will tell us in which order we should compute resources
	for _, resource := range ordered {
		m := predecessorsMap[resource]
		var predecessor apiv1.ResourceName
		if len(m) == 0 {
			continue
		}
		for k := range m { // we are sure that there is only one element here
			predecessor = k
		}
		orderedResult = append(orderedResult, indexedRatio[predecessor+"-"+resource])
	}
	return orderedResult, nil

}

// getSortedResourceAndPredecessors returns an ordered list of nodes (from root to leaves) and also checks that the defined graph is acyclic
func getSortedResourceAndPredecessors(edges resourceRatioList) ([]apiv1.ResourceName, map[apiv1.ResourceName]map[apiv1.ResourceName]struct{}, bool) {
	g := resourceGraph{nodes: map[apiv1.ResourceName]*resourceNode{}}
	for _, edge := range edges {
		g.addEdge(edge.original, edge.target)
	}
	return g.getOrderedListAndPredecessors()
}

type resourceGraph struct {
	nodes map[apiv1.ResourceName]*resourceNode
}

type resourceNode struct {
	key              apiv1.ResourceName
	children, parent map[*resourceNode]struct{}
}

func (g *resourceGraph) addEdge(from, to apiv1.ResourceName) {
	var nodeFrom, nodeTo *resourceNode
	var ok bool
	if nodeTo, ok = g.nodes[to]; !ok {
		nodeTo = &resourceNode{key: to, parent: map[*resourceNode]struct{}{}, children: map[*resourceNode]struct{}{}}
		g.nodes[to] = nodeTo
	}

	if nodeFrom, ok = g.nodes[from]; !ok {
		nodeFrom = &resourceNode{key: from, parent: map[*resourceNode]struct{}{}, children: map[*resourceNode]struct{}{}}
		g.nodes[from] = nodeFrom
	}

	nodeFrom.children[nodeTo] = struct{}{}
	nodeTo.parent[nodeFrom] = struct{}{}
}

// getOrderedListAndPredecessors check that the graph is acyclic and build output like ordered list of resourceNode from root to leaves
// To test a graph for being acyclic:
// 1 - If the graph has no nodes, stop. The graph is acyclic.
// 2 - If the graph has no leaf, stop. The graph is cyclic.
// 3 - Choose a leaf of the graph. Remove this leaf and all arcs going into the leaf to get a new graph.
// Go to 1.
func (g *resourceGraph) getOrderedListAndPredecessors() (orderedList []apiv1.ResourceName, predecessors map[apiv1.ResourceName]map[apiv1.ResourceName]struct{}, acyclic bool) {
	predecessors = map[apiv1.ResourceName]map[apiv1.ResourceName]struct{}{}

	for len(g.nodes) > 0 {
		oneLeafFound := false
		for _, n := range g.nodes {
			if len(n.children) == 0 {
				orderedList = append([]apiv1.ResourceName{n.key}, orderedList...)
				parentKeys := map[apiv1.ResourceName]struct{}{}
				for p := range n.parent {
					parentKeys[p.key] = struct{}{}
					delete(p.children, n)
				}
				predecessors[n.key] = parentKeys
				oneLeafFound = true
				delete(g.nodes, n.key)
				break
			}
		}
		if !oneLeafFound {
			break
		}
	}
	if len(g.nodes) > 0 {
		return nil, nil, false
	}
	return orderedList, predecessors, true
}
