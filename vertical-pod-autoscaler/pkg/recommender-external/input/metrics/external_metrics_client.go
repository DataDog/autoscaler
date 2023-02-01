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

	"k8s.io/apimachinery/pkg/labels"
	externalclient "k8s.io/metrics/pkg/client/external_metrics"

	recommender_metrics "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/metrics/recommender"
)

// MetricsClient provides simple metrics on resources usage on containter level.
type MetricsClient interface {
	// GetExternalMetrics returns FIXME really return stuff
	GetExternalMetric(metricName, namespace string, selector labels.Selector) ([]int64, time.Time, error)
}

// NewMetricsClient creates a new external metric client.
func NewMetricsClient(externalClient externalclient.ExternalMetricsClient, namespace string, clientName string) MetricsClient {
	return &metricsClient{
		client:     externalClient,
		namespace:  namespace,
		clientName: clientName,
	}
}

type metricsClient struct {
	client     externalclient.ExternalMetricsClient
	namespace  string
	clientName string
}

// GetExternalMetric gets all the values of a given external metric
// that match the specified selector.
// TODO: Review all that when we actually use it
func (c *metricsClient) GetExternalMetric(metricName, namespace string, selector labels.Selector) ([]int64, time.Time, error) {
	metrics, err := c.client.NamespacedMetrics(namespace).List(metricName, selector)
	recommender_metrics.RecordMetricsServerResponse(err, c.clientName)
	if err != nil {
		return []int64{}, time.Time{}, fmt.Errorf("unable to fetch metrics from external metrics API: %v", err)
	}

	if len(metrics.Items) == 0 {
		return nil, time.Time{}, fmt.Errorf("no metrics returned from external metrics API")
	}

	res := make([]int64, 0)
	for _, m := range metrics.Items {
		res = append(res, m.Value.MilliValue())
	}
	timestamp := metrics.Items[0].Timestamp.Time
	// TODO: Change (or wrap) this function to return Resources instead.
	return res, timestamp, nil
}
