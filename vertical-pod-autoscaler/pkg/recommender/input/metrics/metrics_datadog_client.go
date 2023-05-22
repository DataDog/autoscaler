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
	"context"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/DataDog/datadog-api-client-go/api/v1/datadog"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"k8s.io/metrics/pkg/apis/metrics/v1beta1"
	"k8s.io/metrics/pkg/client/clientset/versioned/typed/metrics/v1alpha1"
	resourceclient "k8s.io/metrics/pkg/client/clientset/versioned/typed/metrics/v1beta1"
)

type ddclientMetrics struct {
	resourceclient.PodMetricsesGetter
	ddConfig        ConfigDDAPI
	Client          *clientWrapper
	QueryInterval   time.Duration
	ClusterName     string
	ExtraTagsClause string // Something to shove into the query, where it'll add more AND clauses.
}

type ddclientPodMetrics struct {
	v1alpha1.PodMetricsInterface
	ddConfig        ConfigDDAPI
	Client          *clientWrapper
	QueryInterval   time.Duration
	ClusterName     string
	Namespace       string // Namespace for the pods
	ExtraTagsClause string // Something to shove into the query, where it'll add more AND clauses.
}

func (d *ddclientMetrics) PodMetricses(namespace string) resourceclient.PodMetricsInterface {
	klog.Infof("ddclientMetrics.PodMetricses(namespace:%s)", namespace)
	return &ddclientPodMetrics{ddConfig: d.ddConfig, Namespace: namespace, Client: d.Client,
		QueryInterval: d.QueryInterval, ClusterName: d.ClusterName, ExtraTagsClause: d.ExtraTagsClause}
}

func (d *ddclientPodMetrics) queryMetrics(ctx context.Context, start, end time.Time, queryStr string) (datadog.MetricsQueryResponse, error) {
	ctxWithCredential, err := getCtx(ctx, d.ddConfig)
	if err != nil {
		return datadog.MetricsQueryResponse{}, err
	}
	resp, err := d.Client.QueryMetrics(ctxWithCredential, start, end, queryStr)

	return resp, err
}

// Split up a series by a tag key, with a map of value -> subseries.
// Series values that don't have the tag will be under the bucket keyed with emptyTag.
func classifyByTag(values []datadog.MetricsQueryMetadata, tagKey string, emptyTag string) map[string][]datadog.MetricsQueryMetadata {
	result := make(map[string][]datadog.MetricsQueryMetadata)
	tagKey = tagKey + ":"
	valueAt := len(tagKey)
	for _, entity := range values {
		matched := false
		for _, ts := range entity.GetTagSet() {
			if strings.HasPrefix(ts, tagKey) {
				tagValue := ts[valueAt:]
				result[tagValue] = append(result[tagValue], entity)
				matched = true
				continue
			}
		}
		if !matched {
			result[emptyTag] = append(result[emptyTag], entity)
		}
	}
	return result
}

type containerResourceData map[float64]map[string]map[string]float64 // timestamp/container/resourceName

func aggregateResourceData(values map[string][]datadog.MetricsQueryMetadata, resourceName string,
	transform func(datadog.MetricsQueryMetadata, float64) float64,
	dest containerResourceData) {
	for containerName, ress := range values {
		for _, res := range ress {
			for rowIdx, row := range res.Pointlist {
				if len(row) > 1 && row[0] != nil && row[1] != nil {
					timestamp := *row[0]
					value := transform(res, *row[1])
					if dest[timestamp] == nil {
						dest[timestamp] = make(map[string]map[string]float64)
					}
					if dest[timestamp][containerName] == nil {
						dest[timestamp][containerName] = make(map[string]float64)
					}
					dest[timestamp][containerName][resourceName] = value
				} else {
					klog.V(2).Infof("Got short row for container %v: row[%d]=%v",
						containerName, rowIdx, row)
				}
			}
		}
	}
}

// Returns the number of whole hypercores (e.g., a hyperthread in Intel parlance) indicated by this
// raw measurement.
func scaleCpuToCores(met datadog.MetricsQueryMetadata, value float64) float64 {

	// Nanocores have a scale factor of 1e-9.
	scale := met.Unit[0].ScaleFactor
	if scale == nil {
		return value
	}
	return value * *scale
}

// Returns the number of bytes indicated by this raw measurement.
func scaleMemToBytes(met datadog.MetricsQueryMetadata, value float64) float64 {
	// These are always in bytes (scale=1), but let's be resilient.
	scale := met.Unit[0].ScaleFactor
	if scale == nil {
		return value
	}
	return value * *scale
}

func makeResourceList(cpu float64, mem float64) map[v1.ResourceName]resource.Quantity {
	return map[v1.ResourceName]resource.Quantity{
		v1.ResourceCPU:    *resource.NewMilliQuantity(int64(cpu*1000.0), resource.DecimalSI),
		v1.ResourceMemory: *resource.NewQuantity(int64(mem), resource.BinarySI),
	}
}

// This API is only really designed for 1 snapshot value!  My timestamp handling here is mostly a
// problem of not screwing up something simple and subtle.  So, scan the list for the _least_ recent
// timestamp on both cpu and rss.  As we're doing periodic sampling, that's fine.  The most
// recent timestamp may be partial data.
// Presumes that all values are for the same pod (tag: pod_name).
func aggregatePodMetrics(namespace, podName string, cpuResp []datadog.MetricsQueryMetadata,
	memResp []datadog.MetricsQueryMetadata) *v1beta1.PodMetrics {
	// Go by container.
	containersMem := classifyByTag(memResp, "container_name", "unknown-container")
	containersCpu := classifyByTag(cpuResp, "container_name", "unknown-container")

	if namespace == "" {
		var ok bool
		namespace, ok = findFirstTagOccurrence("kube_namespace", cpuResp, memResp)
		if !ok {
			klog.Errorf("Can't extract kube_namespace from series")
			return nil
		}
	}

	// Map of timestamp -> container_name -> resource (apis.metrics.v1.ResourceName: "cpu", "memory") -> value
	data := make(containerResourceData)
	aggregateResourceData(containersMem, "memory", scaleMemToBytes, data)
	aggregateResourceData(containersCpu, "cpu", scaleCpuToCores, data)

	timestamps := make([]float64, 0, len(data))
	for t := range data {
		timestamps = append(timestamps, t)
	}
	sort.Float64s(timestamps)
	selection := -1.0
FindTimestamp:
	for t := 0; t < len(timestamps); t++ {
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
	var containers []v1beta1.ContainerMetrics
	if selection > 0.0 {
		containers = make([]v1beta1.ContainerMetrics, 0, len(data[selection]))
		for name, containerData := range data[selection] {
			containers = append(containers,
				v1beta1.ContainerMetrics{Name: name, Usage: makeResourceList(containerData["cpu"], containerData["memory"])})
		}
	} else {
		containers = make([]v1beta1.ContainerMetrics, 0)
		// Reset the selection value to keep the timestamps sane below.
		selection = 0.0
	}

	return &v1beta1.PodMetrics{
		// Built against golang 1.16.6, no time.UnixMilli yet.
		ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: namespace, UID: types.UID(namespace + "." + podName)},
		Timestamp:  metav1.Time{Time: time.Unix(int64(selection/1000.0), int64(math.Mod(selection, 1000)*1000000))},
		Window:     metav1.Duration{Duration: time.Second},
		Containers: containers}
}

func findFirstTagOccurrence(tagKey string, ddMetrics ...[]datadog.MetricsQueryMetadata) (string, bool) {
	tagKey = tagKey + ":"
	for _, list := range ddMetrics {
		for _, item := range list {
			for _, tag := range item.GetTagSet() {
				if strings.HasPrefix(tag, tagKey) {
					return tag[len(tagKey):], true
				}
			}
		}
	}
	return "", false
}

func (d *ddclientPodMetrics) Get(ctx context.Context, podName string, _ metav1.GetOptions) (*v1beta1.PodMetrics, error) {
	nsClause := ""
	if len(d.Namespace) > 0 {
		nsClause = fmt.Sprintf(" AND kube_namespace:%s ", d.Namespace)
	}

	start := time.Now()
	end := start.Add(d.QueryInterval)

	cpuResp, err := d.queryMetrics(ctx, start, end, fmt.Sprintf("kubernetes.cpu.usage.total{kube_cluster_name:%s%s%s AND pod_name:%s}by{kube_namespace,container_name}",
		d.ClusterName, nsClause, d.ExtraTagsClause, podName))
	if err != nil {
		return nil, err
	}
	memResp, err := d.queryMetrics(ctx, start, end, fmt.Sprintf("kubernetes.memory.usage{ kube_cluster_name:%s%s%s AND pod_name:%s}by{kube_namespace,container_name}",
		d.ClusterName, nsClause, d.ExtraTagsClause, podName))
	if err != nil {
		return nil, err
	}

	return aggregatePodMetrics(d.Namespace, podName, cpuResp.GetSeries(), memResp.GetSeries()), nil
}

func (d *ddclientPodMetrics) List(ctx context.Context, _ metav1.ListOptions) (*v1beta1.PodMetricsList, error) {
	nsClause := ""
	if len(d.Namespace) > 0 {
		nsClause = fmt.Sprintf(" AND kube_namespace:%s ", d.Namespace)
	}

	start := time.Now()
	end := start.Add(d.QueryInterval)

	cpuResp, err := d.queryMetrics(ctx, start, end, fmt.Sprintf("kubernetes.cpu.usage.total{kube_cluster_name:%s%s%s}by{kube_namespace,pod_name,container_name}",
		d.ClusterName, nsClause, d.ExtraTagsClause))
	if err != nil {
		return nil, err
	}
	memResp, err := d.queryMetrics(ctx, start, end, fmt.Sprintf("kubernetes.memory.usage{kube_cluster_name:%s%s%s}by{kube_namespace,pod_name,container_name}",
		d.ClusterName, nsClause, d.ExtraTagsClause))
	if err != nil {
		return nil, err
	}

	// TODO there is a potential issue here as we are losing the namespace! STS pod can have the same name in different namespaces!
	// This function is called most of the time (always) with d.namespace=""
	return buildPodMetricsListFromDDResponses(d.Namespace, cpuResp, memResp)
}

func buildPodMetricsListFromDDResponses(namespace string, cpuResp datadog.MetricsQueryResponse, memResp datadog.MetricsQueryResponse) (*v1beta1.PodMetricsList, error) {
	podCpus := classifyByTag(cpuResp.GetSeries(), "pod_name", "unknown-pod")
	podMems := classifyByTag(memResp.GetSeries(), "pod_name", "unknown-pod")

	// already here we have a problem with the assumption that the list for CPU and Mem is the same
	podItems := make([]v1beta1.PodMetrics, 0, len(podCpus))
	for podname, cpuVals := range podCpus {
		podMets := aggregatePodMetrics(namespace, podname, cpuVals, podMems[podname])
		if podMets != nil {
			podItems = append(podItems, *podMets)
		}
	}

	return &v1beta1.PodMetricsList{Items: podItems}, nil
}

func newDatadogClientWithFactory(queryInterval time.Duration, cluster string, extraTags []string, newApiClient func(*datadog.Configuration) *clientWrapper) resourceclient.PodMetricsesGetter {
	configuration := datadog.NewConfiguration()
	apiClient := newApiClient(configuration)
	klog.V(2).Infof("NewDatadogClient(%v, %s, site=%s)", queryInterval, cluster, apiClient.config.DDurl)
	clause := ""
	if extraTags != nil && len(extraTags) > 1 {
		validTag, _ := regexp.Compile("[a-zA-Z0-9-]+:[a-zA-Z0-9-]")
		filtered := make([]string, 0, len(extraTags))
		for _, s := range extraTags {
			s = strings.TrimSpace(s)
			if len(s) > 0 && validTag.MatchString(s) {
				filtered = append(filtered, s)
			}
		}
		if len(filtered) > 0 {
			clause = " AND " + strings.Join(filtered, " AND ")
		}

	}
	return &ddclientMetrics{Client: apiClient, QueryInterval: queryInterval, ClusterName: cluster, ExtraTagsClause: clause}
}

type clientWrapper struct {
	ApiClient datadog.APIClient
	config    ConfigDDAPI
}

func (c *clientWrapper) QueryMetrics(context context.Context, start, end time.Time, query string) (datadog.MetricsQueryResponse, error) {
	ctx, err := getCtx(context, c.config)
	if err != nil {
		return datadog.MetricsQueryResponse{}, err
	}
	resp, httpResponse, err := c.ApiClient.MetricsApi.QueryMetrics(ctx, start.Unix(), end.Unix(), query)

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error when calling `MetricsApi.QueryMetrics` on %s: %v\n", query, err)
		fmt.Fprintf(os.Stderr, "Full HTTP response: %v\n", httpResponse)
		return datadog.MetricsQueryResponse{}, err
	}
	klog.V(1).Infof("queryMetrics('%s'): got response with %d series from %d to %d", query, len(resp.GetSeries()), resp.GetFromDate(), resp.GetToDate())

	if httpResponse == nil {
		err = fmt.Errorf("nil HTTPResponse from datadog QueryMetrics")
		klog.Errorf(err.Error())
		return datadog.MetricsQueryResponse{}, err
	}

	headers := httpResponse.Header
	// http.Header is a map[string][]string
	for k, vs := range headers {
		if strings.HasPrefix(strings.ToLower(k), "x-ratelimit") {
			klog.V(2).Infof("Query header: %s, %v", k, vs)
		}
	}
	return resp, err
}

// NewDatadogClient creates a Datadog API client and wraps it up as a PodMetricsesGetter.
func NewDatadogClient(queryInterval time.Duration, cluster string, extraTags []string) resourceclient.PodMetricsesGetter {
	cfg, err := getConfigDDAPI()
	if err != nil {
		panic(err)
	}
	var wrapFn = func(configuration *datadog.Configuration) *clientWrapper {
		return &clientWrapper{ApiClient: *datadog.NewAPIClient(configuration), config: cfg}
	}
	return newDatadogClientWithFactory(queryInterval, cluster, extraTags, wrapFn)
}
