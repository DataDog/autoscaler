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
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/labels"
)

func TestGetEmptyMetric(t *testing.T) {
	tc := NewEmptyMetricsClientTestCase()
	emptyMetricsClient := tc.CreateFakeMetricsClient()

	_, _, err := emptyMetricsClient.GetExternalMetric("qps", "fake", labels.Everything())

	assert.Error(t, err)
}

func TestGetMetric(t *testing.T) {
	tc := NewMetricsClientTestCase()
	fakeMetricsClient := tc.CreateFakeMetricsClient()

	value, time, err := fakeMetricsClient.GetExternalMetric("qps", "fake", labels.Everything())

	assert.NoError(t, err)
	assert.Equal(t, int64(1), value)
	assert.NotZero(t, time)
}
