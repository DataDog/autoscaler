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
	"k8s.io/klog/v2"
	externalclient "k8s.io/metrics/pkg/client/external_metrics"

	recommender_metrics "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/metrics/recommender"
)

// MetricsClient provides simple metrics on resources usage on container level. It always returns the last point only.
type MetricsClient interface {
	GetExternalMetric(metricName, namespace string, selector labels.Selector) (int64, time.Time, error)
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
func (c *metricsClient) GetExternalMetric(metricName, namespace string, selector labels.Selector) (int64, time.Time, error) {
	metrics, err := c.client.NamespacedMetrics(namespace).List(metricName, selector)
	recommender_metrics.RecordMetricsServerResponse(err, c.clientName)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("unable to fetch metrics from external metrics API: %v", err)
	}

	if len(metrics.Items) == 0 {
		return 0, time.Time{}, fmt.Errorf("no metrics returned from external metrics API")
	}

	klog.V(6).Infof("Got %d points: %+v", len(metrics.Items), metrics.Items)

	idx := len(metrics.Items) - 1
	value := metrics.Items[idx].Value.Value()
	timestamp := metrics.Items[idx].Timestamp.Time

	return value, timestamp, nil
}
