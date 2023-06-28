/*
Copyright 2017 The Kubernetes Authors.

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

package metrics

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/DataDog/datadog-api-client-go/api/v1/datadog"
	"github.com/stretchr/testify/assert"
	"k8s.io/metrics/pkg/apis/metrics/v1beta1"

	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
)

func readFileInto(t *testing.T, filename string, v interface{}) {
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("can't read test file: %v", err)
		return
	}
	if err := json.Unmarshal([]byte(data), v); err != nil {
		t.Fatalf("can't read test data: %v", err)
		return
	}
}

func Test_createPodMetrics(t *testing.T) {
	var metricsPerResource map[model.ResourceName][]datadog.MetricsQueryMetadata
	readFileInto(t, "test/metricsPerResource.json", &metricsPerResource)
	var expectedPodMetrics v1beta1.PodMetrics
	readFileInto(t, "test/podMetric.json", &expectedPodMetrics)

	podMetric := createPodMetrics("", "disruption-budget-manager-6d758d9d58-6pbpd", metricsPerResource)
	if podMetric == nil {
		t.Fatalf("nil podMetric")
	}
	b, err := json.Marshal(podMetric)
	if err != nil {
		t.Fatalf(err.Error())
	}
	var podMetricsAfterMarshall v1beta1.PodMetrics
	if err := json.Unmarshal(b, &podMetricsAfterMarshall); err != nil {
		if err != nil {
			t.Fatalf(err.Error())
		}
	}

	sort.Slice(podMetricsAfterMarshall.Containers, func(i, j int) bool {
		return strings.Compare(podMetricsAfterMarshall.Containers[i].Name, podMetricsAfterMarshall.Containers[j].Name) < 0
	})
	sort.Slice(expectedPodMetrics.Containers, func(i, j int) bool {
		return strings.Compare(expectedPodMetrics.Containers[i].Name, expectedPodMetrics.Containers[j].Name) < 0
	})

	assert.Equal(t, podMetricsAfterMarshall, expectedPodMetrics)
}
