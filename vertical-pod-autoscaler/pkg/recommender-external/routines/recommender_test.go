/*
Copyright 2023 The Kubernetes Authors.

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
	"testing"

	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes/fake"
	v1lister "k8s.io/client-go/listers/core/v1"

	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender-external"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender-external/input"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender-external/model"

	v1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	vpa_fake "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/clientset/versioned/fake"
	vpa_lister "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/listers/autoscaling.k8s.io/v1"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender-external/logic"
	upstream_input "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/input"
	controllerfetcher "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/input/controller_fetcher"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/input/oom"
	upstream_model "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
	upstream_routines "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/routines"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/test"
)

// Implements ControllerFetcher
type controllerFetcherMock struct{}

func (c *controllerFetcherMock) FindTopMostWellKnownOrScalable(controller *controllerfetcher.ControllerKeyWithAPIVersion) (*controllerfetcher.ControllerKeyWithAPIVersion, error) {
	return nil, nil
}

// Implements PodLister
type podListerMock struct{}

// List lists all Pods in the indexer.
// Objects returned here must be treated as read-only.
func (l *podListerMock) List(selector labels.Selector) (ret []*apiv1.Pod, err error) {
	pod := test.Pod().WithName("pod").AddContainer(test.BuildTestContainer("container", "4", "")).Get()

	ret = make([]*apiv1.Pod, 0)
	ret = append(ret, pod)
	return ret, nil
}

func (l *podListerMock) Get(name string) (*apiv1.Pod, error) {
	pod := test.Pod().WithName(name).AddContainer(test.BuildTestContainer("container", "4", "")).Get()
	return pod, nil
}

// Pods returns an object that can list and get Pods.
func (l *podListerMock) Pods(namespace string) v1lister.PodNamespaceLister {
	return l
}

// Implements VerticalPodAutoscalerLister and VpaTargetSelectorFetcher
type vpaListerMock struct{}

func (l *vpaListerMock) List(selector labels.Selector) (ret []*v1.VerticalPodAutoscaler, err error) {
	vpas := make([]*v1.VerticalPodAutoscaler, 0)
	return vpas, nil
}

func (l *vpaListerMock) Get(name string) (*v1.VerticalPodAutoscaler, error) {
	vpa := &v1.VerticalPodAutoscaler{}
	return vpa, nil
}

func (l *vpaListerMock) Fetch(vpa *vpa_types.VerticalPodAutoscaler) (labels.Selector, error) {
	selector := labels.Everything()
	return selector, nil
}

func (l *vpaListerMock) VerticalPodAutoscalers(namespace string) vpa_lister.VerticalPodAutoscalerNamespaceLister {
	return l
}

// Implements Observer
type oomObserverMock struct {
	oomChannel chan oom.OomInfo
}

func (o *oomObserverMock) GetObservedOomsChannel() chan oom.OomInfo {
	return o.oomChannel
}

func (o *oomObserverMock) OnEvent(*apiv1.Event) {
}

func (o *oomObserverMock) OnAdd(obj interface{}) {
}

func (o *oomObserverMock) OnUpdate(oldObj, newObj interface{}) {
}

func (o *oomObserverMock) OnDelete(obj interface{}) {
}

func NewOomObserverMock() *oomObserverMock {
	return &oomObserverMock{oomChannel: make(chan oom.OomInfo)}
}

// TestBasic is a very basic test to validate that we can instantiate a recommender.
// TODO: Build a real integration test.
func TestBasic(t *testing.T) {
	clusterState := upstream_model.NewClusterState(0)
	recommenderName := main.DefaultRecommenderName
	var postProcessors []upstream_routines.RecommendationPostProcessor

	controllerFetcher := &controllerFetcherMock{}

	podLister := &podListerMock{}
	vpaLister := &vpaListerMock{}
	oomObserver := NewOomObserverMock()

	kubeClient := &fake.Clientset{}
	fakeScalingClient := vpa_fake.NewSimpleClientset(&vpa_types.VerticalPodAutoscalerList{Items: []vpa_types.VerticalPodAutoscaler{}})

	clusterStateFeeder := upstream_input.ClusterStateFeederFactory{
		PodLister:           podLister,
		OOMObserver:         oomObserver,
		KubeClient:          kubeClient,
		MetricsClient:       nil,
		VpaCheckpointClient: nil,
		VpaLister:           vpaLister,
		ClusterState:        clusterState,
		SelectorFetcher:     vpaLister,
		MemorySaveMode:      true,
		ControllerFetcher:   controllerFetcher,
		RecommenderName:     recommenderName,
	}.Make()

	externalRecommendationState := model.NewExternalRecommendationsState(AggregateContainerStateGCInterval)
	exernalRecommendationStateFetcher := input.ExternalRecommendationsStateFeederFactory{
		ExternalRecommendationsState: externalRecommendationState,
		ClusterState:                 clusterState,
		MetricsClient:                nil, // FIXME
	}.Make()

	recommender := RecommenderFactory{
		ClusterState:                       clusterState,
		ClusterStateFeeder:                 clusterStateFeeder,
		ControllerFetcher:                  controllerFetcher,
		ExternalRecommendationsState:       externalRecommendationState,
		ExternalRecommendationsStateFeeder: exernalRecommendationStateFetcher,
		VpaClient:                          fakeScalingClient.AutoscalingV1(),
		PodResourceRecommenderFactory:      logic.NewPodResourceRecommenderFactory(),
		RecommendationPostProcessors:       postProcessors,
	}.Make()

	recommender.RunOnce()
}
