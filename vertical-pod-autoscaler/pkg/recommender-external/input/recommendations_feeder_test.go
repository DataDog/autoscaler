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

package input

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"k8s.io/apimachinery/pkg/labels"

	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender-external/input/metrics"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender-external/model"
	upstream_model "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
)

const (
	testGcPeriod  = time.Minute
	ContainerName = "container-1"
)

var (
	VpaID1 = upstream_model.VpaID{VpaName: fmt.Sprintf("test-vpa-1"), Namespace: "default"}
	VpaID2 = upstream_model.VpaID{VpaName: fmt.Sprintf("test-vpa-2"), Namespace: "default"}
)

func createFeeder(t *testing.T) *externalRecommendationsStateFeeder {
	clusterState := upstream_model.NewClusterState(testGcPeriod)

	podSelector1, err := labels.Parse("name=vpa-pod-1")
	assert.NoError(t, err)
	podSelector2, err := labels.Parse("name=vpa-pod-2")
	assert.NoError(t, err)

	clusterState.Vpas = map[upstream_model.VpaID]*upstream_model.Vpa{
		VpaID1: {
			ID:          VpaID1,
			PodSelector: podSelector1,
			Annotations: map[string]string{
				AnnotationKey(ContainerName, upstream_model.ResourceCPU):    "system.cpu",
				AnnotationKey(ContainerName, upstream_model.ResourceMemory): "system.mem",
			},
		},
		VpaID2: {
			ID:          VpaID2,
			PodSelector: podSelector2,
		},
	}

	externalRecommendationState := model.NewExternalRecommendationsState(testGcPeriod)
	feeder := externalRecommendationsStateFeeder{
		clusterState:                 clusterState,
		externalRecommendationsState: externalRecommendationState,
		metricsClient:                metrics.NewMetricsClientTestCase().CreateFakeMetricsClient(),
	}
	return &feeder
}
func TestExternalRecommendationsFeeder_LoadVPAs(t *testing.T) {
	feeder := createFeeder(t)

	feeder.LoadVPAs()
	assert.Len(t, feeder.externalRecommendationsState.Vpas, len(feeder.clusterState.Vpas))
}

func TestExternalRecommendationsFeeder_LoadMetrics(t *testing.T) {
	feeder := createFeeder(t)

	feeder.LoadVPAs()
	feeder.LoadMetrics()

	vpaId := upstream_model.VpaID{Namespace: "default", VpaName: "test-vpa-1"}

	expected := upstream_model.Resources{
		upstream_model.ResourceCPU:    upstream_model.ResourceAmount(1),
		upstream_model.ResourceMemory: upstream_model.ResourceAmount(1),
	}
	assert.Contains(t, feeder.externalRecommendationsState.Vpas, vpaId)
	assert.Contains(t, feeder.externalRecommendationsState.Vpas[vpaId].Containers, ContainerName)
	assert.Equal(t, expected, feeder.externalRecommendationsState.Vpas[vpaId].Containers[ContainerName].RawRecommendation)
}
