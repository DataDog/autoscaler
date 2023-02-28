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

package metrics

import (
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	core "k8s.io/client-go/testing"
	emapi "k8s.io/metrics/pkg/apis/external_metrics/v1beta1"
	emfake "k8s.io/metrics/pkg/client/external_metrics/fake"
)

type metricsQueryTestCase struct {
	value int64
	time  time.Time
	error error
}
type metricsClientTestCase struct {
	metrics map[string]metricsQueryTestCase
}

// NewMetricsClientTestCase creates a new test case with pre-defined metrics.
func NewMetricsClientTestCase() *metricsClientTestCase {

	testCase := &metricsClientTestCase{
		metrics: make(map[string]metricsQueryTestCase),
	}

	testCase.metrics["qps"] = metricsQueryTestCase{
		value: 1,
		time:  time.Now(),
		error: nil,
	}
	testCase.metrics["system.cpu"] = metricsQueryTestCase{
		value: 1,
		time:  time.Now(),
		error: nil,
	}
	testCase.metrics["system.mem"] = metricsQueryTestCase{
		value: 1,
		time:  time.Now(),
		error: nil,
	}
	return testCase
}

// NewEmptyMetricsClientTestCase creates a new  test cacse with no metrics.
func NewEmptyMetricsClientTestCase() *metricsClientTestCase {
	return &metricsClientTestCase{}
}

func (tc *metricsClientTestCase) CreateFakeMetricsClient() MetricsClient {
	fakeEMClient := &emfake.FakeExternalMetricsClient{}
	fakeEMClient.AddReactor("list", "*", func(action core.Action) (handled bool, ret runtime.Object, err error) {
		listAction, wasList := action.(core.ListAction)
		if !wasList {
			return true, nil, fmt.Errorf("expected a list action, got %v instead", action)
		}

		metrics := &emapi.ExternalMetricValueList{}

		q := listAction.GetResource().Resource

		value, found := tc.metrics[q]

		if found && value.error == nil {
			metric := emapi.ExternalMetricValue{
				Timestamp:  metav1.Time{Time: value.time},
				MetricName: q,
				Value:      *resource.NewMilliQuantity(int64(value.value), resource.DecimalSI),
			}
			metrics.Items = append(metrics.Items, metric)
		}

		return true, metrics, value.error
	})
	return NewMetricsClient(fakeEMClient, "", "fake")
}
