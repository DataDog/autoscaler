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

package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	datadog "github.com/DataDog/datadog-api-client-go/api/v1/datadog"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/metrics/pkg/apis/metrics/v1beta1"
	"k8s.io/metrics/pkg/client/clientset/versioned/typed/metrics/v1alpha1"
	//_ "k8s.io/metrics/pkg/client/clientset/versioned/typed/metrics/v1beta1"
	resourceclient "k8s.io/metrics/pkg/client/clientset/versioned/typed/metrics/v1beta1"
	"math"
	"os"
	"regexp"
	"sort"
	"time"
)

type ddclientMetrics struct {
	resourceclient.PodMetricsesGetter
	Context       context.Context
	Client        *datadog.APIClient
	QueryInterval time.Duration
	ClusterName   string
}

type ddclientPodMetrics struct {
	v1alpha1.PodMetricsInterface
	Context       context.Context
	Client        *datadog.APIClient
	QueryInterval time.Duration
	ClusterName   string
	Namespace     string
}

func (d ddclientMetrics) PodMetricses(namespace string) resourceclient.PodMetricsInterface {
	return &ddclientPodMetrics{Namespace: namespace, Client: d.Client, QueryInterval: d.QueryInterval, ClusterName: d.ClusterName}
}

func (d ddclientPodMetrics) queryMetrics(queryStr string) (datadog.MetricsQueryResponse, error) {
	resp, r, err := d.Client.MetricsApi.QueryMetrics(d.Context, time.Now().Add(d.QueryInterval).Unix(), time.Now().Unix(),
		queryStr)

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error when calling `MetricsApi.QueryMetrics`: %v\n", err)
		fmt.Fprintf(os.Stderr, "Full HTTP response: %v\n", r)
	}

	return resp, err
}

// Split up a series by a tag key, with a map of value -> subseries.
// Series values that don't have the tag will be under the bucket keyed with emptyTag.
func classifyByTag(values []datadog.MetricsQueryMetadata, tag string, emptyTag string) map[string][]datadog.MetricsQueryMetadata {
	result := make(map[string][]datadog.MetricsQueryMetadata)
	re := regexp.MustCompile(tag + ":(.*)")
	for _, entity := range values {
		for _, ts := range entity.GetTagSet() {
			match := re.FindStringSubmatch(ts)
			if match != nil {
				result[match[0]] = append(result[match[0]], entity)
			} else {
				result[emptyTag] = append(result[emptyTag], entity)
			}
		}
	}
	return result
}

type containerResourceData map[float64]map[string]map[string]float64

func aggregateResourceData(values map[string][]datadog.MetricsQueryMetadata, resourceName string,
	transform func(datadog.MetricsQueryMetadata, float64) float64,
	dest *containerResourceData) {
	for containerName, ress := range values {
		for _, res := range ress {
			for _, row := range res.Pointlist {
				timestamp := *row[0]
				value := transform(res, *row[1])
				(*dest)[timestamp][containerName][resourceName] = value
			}
		}
	}
}

// Returns the number of whole hypercores (e.g., a hyperthread in Intel parlance) indicated by this
// raw measurement.
func scaleCpuToCores(met datadog.MetricsQueryMetadata, value float64) float64 {
	// Nanocores have a scale factor of 1e-9.
	scale := (met.Unit)[0].ScaleFactor
	return value * *scale
}

// Returns the number of bytes indicated by this raw measurement.
func scaleMemToBytes(met datadog.MetricsQueryMetadata, value float64) float64 {
	// These are always in bytes (scale=1), but let's be resilient.
	scale := (met.Unit)[0].ScaleFactor
	return value * *scale
}

func makeResourceList(cpu float64, mem float64) map[v1.ResourceName]resource.Quantity {
	return map[v1.ResourceName]resource.Quantity{
		v1.ResourceCPU:    *resource.NewMilliQuantity(int64(cpu*1000.0), resource.DecimalSI),
		v1.ResourceMemory: *resource.NewQuantity(int64(mem), resource.BinarySI),
	}
}

// This API is only really designed for 1 snapshot value!  My timestamp handling here is mostly a
// problem of not screwing up something simple and subtle.  So, scan the list for the _most_ recent
// timestamp on both cpu and rss.
// Presumes that all values are for the same pod (tag: pod_name).
func (d ddclientPodMetrics) aggregatePodMetrics(cpuResp []datadog.MetricsQueryMetadata,
	memResp []datadog.MetricsQueryMetadata) *v1beta1.PodMetrics {
	// Go by container.
	containersMem := classifyByTag(memResp, "container_name", "unknown-container")
	containersCpu := classifyByTag(cpuResp, "container_name", "unknown-container")

	// Map of timestamp -> container_name -> resource (apis.metrics.v1.ResourceName: "cpu", "memory") -> value
	data := make(containerResourceData)
	aggregateResourceData(containersMem, "memory", scaleMemToBytes, &data)
	aggregateResourceData(containersCpu, "cpu", scaleCpuToCores, &data)

	timestamps := make([]float64, 0, len(data))
	for t := range data {
		timestamps = append(timestamps, t)
	}
	sort.Float64s(timestamps)
	selection := -1.0
FindTimestamp:
	for t := len(timestamps) - 1; t >= 0; t-- {
		ts := data[timestamps[t]]
		for container := range ts {
			if _, mem := ts[container]["memory"]; mem {
				if _, cpu := ts[container]["cpu"]; !cpu {
					// Note the '!' above.  This is if there's no cpu while there is mem.
					continue
				}
			} else {
				// no mem.
				continue
			}
			selection = timestamps[t]
			break FindTimestamp
		}
	}
	if selection > 0.0 {
		containers := make([]v1beta1.ContainerMetrics, 0, len(data[selection]))
		for name, containerData := range data[selection] {
			containers = append(containers,
				v1beta1.ContainerMetrics{Name: name, Usage: makeResourceList(containerData["cpu"], containerData["memory"])})
		}
		return &v1beta1.PodMetrics{
			// Built against golang 1.16.6, no time.UnixMilli yet.
			Timestamp:  metav1.Time{Time: time.Unix(int64(selection/1000.0), int64(math.Mod(selection, 1000)*1000000))},
			Window:     metav1.Duration{Duration: time.Second},
			Containers: containers}

	}

	return nil
}

func (d ddclientPodMetrics) Get(_ context.Context, podName string, _ metav1.GetOptions) (*v1beta1.PodMetrics, error) {
	// Metrics known to work:
	//   kubernetes.cpu.usage.total
	//   kubernetes.cpu.requests
	//   kubernetes.memory.usage
	//   kubernetes.memory.rss -- From comment in v1alpha1/types.go:93: "THe memory usage is the memory working set"
	//   kubernetes.memory.requests
	cpuResp, err := d.queryMetrics(fmt.Sprintf("kubernetes.cpu.usage.total{kube_cluster_name:%s AND kube_namespace:%s AND pod_name:%s}",
		d.ClusterName, d.Namespace, podName))
	if err != nil {
		return nil, err
	}
	memResp, err := d.queryMetrics(fmt.Sprintf("kubernetes.memory.usage{ kube_cluster_name:%s AND kube_namespace:%s AND pod_name:%s}",
		d.ClusterName, d.Namespace, podName))
	if err != nil {
		return nil, err
	}

	return d.aggregatePodMetrics(cpuResp.GetSeries(), memResp.GetSeries()), nil
}

func (d ddclientPodMetrics) List(_ context.Context, _ metav1.ListOptions) (*v1beta1.PodMetricsList, error) {
	cpuResp, err := d.queryMetrics(fmt.Sprintf("kubernetes.cpu.usage.total{kube_cluster_name:%s AND kube_namespace:%s}",
		d.ClusterName, d.Namespace))
	if err != nil {
		return nil, err
	}
	memResp, err := d.queryMetrics(fmt.Sprintf("kubernetes.memory.usage{kube_cluster_name:%s AND kube_namespace:%s}",
		d.ClusterName, d.Namespace))
	if err != nil {
		return nil, err
	}

	podCpus := classifyByTag(cpuResp.GetSeries(), "pod_name", "unknown-pod")
	podMems := classifyByTag(memResp.GetSeries(), "pod_name", "unknown-pod")

	podItems := make([]v1beta1.PodMetrics, 0, len(podCpus))
	for podname, cpuVals := range podCpus {
		podItems = append(podItems, *d.aggregatePodMetrics(cpuVals, podMems[podname]))
	}

	return &v1beta1.PodMetricsList{Items: podItems}, nil
}

// NewDatadogClient create a new PodMetricsesGetter backed by Datadog API
func NewDatadogClient(queryInterval time.Duration, cluster string) resourceclient.PodMetricsesGetter {
	ctx := datadog.NewDefaultContext(context.Background())
	configuration := datadog.NewConfiguration()
	apiClient := datadog.NewAPIClient(configuration)
	// TODO: Take the tags like pod_name and put them in args here, because we take them in as args.
	resp, r, err := apiClient.MetricsApi.QueryMetrics(ctx, time.Now().AddDate(0, 0, -1).Unix(), time.Now().Unix(), "system.cpu.idle{*}")

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error when calling `MetricsApi.QueryMetrics`: %v\n", err)
		fmt.Fprintf(os.Stderr, "Full HTTP response: %v\n", r)
	}

	responseContent, _ := json.MarshalIndent(resp, "", "  ")
	fmt.Fprintf(os.Stdout, "Response from `MetricsApi.QueryMetrics`:\n%s\n", responseContent)
	return ddclientMetrics{Client: apiClient, QueryInterval: -queryInterval, ClusterName: cluster}
}
