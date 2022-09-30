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
	"io/ioutil"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"k8s.io/metrics/pkg/apis/metrics/v1beta1"
	"k8s.io/metrics/pkg/client/clientset/versioned/typed/metrics/v1alpha1"
	// For updated pod metrics.
	_ "k8s.io/metrics/pkg/client/clientset/versioned/typed/metrics/v1beta1"
	resourceclient "k8s.io/metrics/pkg/client/clientset/versioned/typed/metrics/v1beta1"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	appURLPrefix = "app."
)

type baseClient interface {
	QueryMetrics(context context.Context, interval time.Duration, query string) (datadog.MetricsQueryResponse, *http.Response, error)
	GetRateLimitStats() error
}

type ddclientMetrics struct {
	resourceclient.PodMetricsesGetter
	Context         context.Context
	Client          baseClient
	QueryInterval   time.Duration
	ClusterName     string
	ExtraTagsClause string // Something to shove into the query, where it'll add more AND clauses.
}

type ddclientPodMetrics struct {
	v1alpha1.PodMetricsInterface
	Context         context.Context
	Client          baseClient
	QueryInterval   time.Duration
	ClusterName     string
	Namespace       string // Namespace for the pods
	ExtraTagsClause string // Something to shove into the query, where it'll add more AND clauses.
}

func (d ddclientMetrics) PodMetricses(namespace string) resourceclient.PodMetricsInterface {
	klog.Infof("ddclientMetrics.PodMetricses(namespace:%s)", namespace)
	return &ddclientPodMetrics{Context: d.Context, Namespace: namespace, Client: d.Client,
		QueryInterval: d.QueryInterval, ClusterName: d.ClusterName, ExtraTagsClause: d.ExtraTagsClause}
}

func (d ddclientPodMetrics) queryMetrics(queryStr string) (datadog.MetricsQueryResponse, error) {
	resp, r, err := d.Client.QueryMetrics(d.Context, d.QueryInterval, queryStr)

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error when calling `MetricsApi.QueryMetrics` on %s: %v\n", queryStr, err)
		fmt.Fprintf(os.Stderr, "Full HTTP response: %v\n", r)
	} else {
		klog.V(1).Infof("queryMetrics('%s'): got response with %d series from %d to %d", queryStr, len(resp.GetSeries()), resp.GetFromDate(), resp.GetToDate())
	}

	return resp, err
}

// Split up a series by a tag key, with a map of value -> subseries.
// Series values that don't have the tag will be under the bucket keyed with emptyTag.
func classifyByTag(values []datadog.MetricsQueryMetadata, tag string, emptyTag string) map[string][]datadog.MetricsQueryMetadata {
	result := make(map[string][]datadog.MetricsQueryMetadata)
	tagLen := len(tag)
	for _, entity := range values {
		matched := false
		for _, ts := range entity.GetTagSet() {
			if strings.HasPrefix(ts, tag) && ts[tagLen] == ':' {
				tagValue := ts[tagLen+1:]
				result[tagValue] = append(result[tagValue], entity)
				matched = true
			}
		}
		if !matched {
			result[emptyTag] = append(result[emptyTag], entity)
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
			for rowIdx, row := range *res.Pointlist {
				if len(row) > 1 && row[0] != nil && row[1] != nil {
					timestamp := *row[0]
					value := transform(res, *row[1])
					if (*dest)[timestamp] == nil {
						(*dest)[timestamp] = make(map[string]map[string]float64)
					}
					if (*dest)[timestamp][containerName] == nil {
						(*dest)[timestamp][containerName] = make(map[string]float64)
					}
					(*dest)[timestamp][containerName][resourceName] = value
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
// problem of not screwing up something simple and subtle.  So, scan the list for the _least_ recent
// timestamp on both cpu and rss.  As we're doing periodic sampling, that's fine.  The most
// recent timestamp may be partial data.
// Presumes that all values are for the same pod (tag: pod_name).
func (d ddclientPodMetrics) aggregatePodMetrics(podName string, cpuResp []datadog.MetricsQueryMetadata,
	memResp []datadog.MetricsQueryMetadata) *v1beta1.PodMetrics {
	// Go by container.
	containersMem := classifyByTag(memResp, "container_name", "unknown-container")
	containersCpu := classifyByTag(cpuResp, "container_name", "unknown-container")

	// Pull a kube namespace out of the pod tags.
	podNamespaces := classifyByTag(memResp, "kube_namespace", "")
	podNamespace := ""
	// The whole pod (and all its containers) is all in one kube namespace.  Grab the keys out of podNamespaces
	// and look for one that's nonempty.
	for ns := range podNamespaces {
		if len(ns) > len(podNamespace) {
			podNamespace = ns
		}
	}

	if len(podNamespace) == 0 && len(d.Namespace) > 0 {
		podNamespace = d.Namespace
	}

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
		ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: podNamespace, UID: types.UID(podNamespace + "." + podName)},
		Timestamp:  metav1.Time{Time: time.Unix(int64(selection/1000.0), int64(math.Mod(selection, 1000)*1000000))},
		Window:     metav1.Duration{Duration: time.Second},
		Containers: containers}

}

func (d ddclientPodMetrics) Get(_ context.Context, podName string, _ metav1.GetOptions) (*v1beta1.PodMetrics, error) {
	nsClause := ""
	if len(d.Namespace) > 0 {
		nsClause = fmt.Sprintf(" AND kube_namespace:%s ", d.Namespace)
	}
	cpuResp, err := d.queryMetrics(fmt.Sprintf("kubernetes.cpu.usage.total{kube_cluster_name:%s%s%s AND pod_name:%s}by{kube_namespace,container_name}",
		d.ClusterName, nsClause, d.ExtraTagsClause, podName))
	if err != nil {
		return nil, err
	}
	memResp, err := d.queryMetrics(fmt.Sprintf("kubernetes.memory.usage{ kube_cluster_name:%s%s%s AND pod_name:%s}by{kube_namespace,container_name}",
		d.ClusterName, nsClause, d.ExtraTagsClause, podName))
	if err != nil {
		return nil, err
	}

	return d.aggregatePodMetrics(podName, cpuResp.GetSeries(), memResp.GetSeries()), nil
}

func (d ddclientPodMetrics) List(_ context.Context, _ metav1.ListOptions) (*v1beta1.PodMetricsList, error) {
	nsClause := ""
	if len(d.Namespace) > 0 {
		nsClause = fmt.Sprintf(" AND kube_namespace:%s ", d.Namespace)
	}
	cpuResp, err := d.queryMetrics(fmt.Sprintf("kubernetes.cpu.usage.total{kube_cluster_name:%s%s%s}by{kube_namespace,pod_name,container_name}",
		d.ClusterName, nsClause, d.ExtraTagsClause))
	if err != nil {
		return nil, err
	}
	memResp, err := d.queryMetrics(fmt.Sprintf("kubernetes.memory.usage{kube_cluster_name:%s%s%s}by{kube_namespace,pod_name,container_name}",
		d.ClusterName, nsClause, d.ExtraTagsClause))
	if err != nil {
		return nil, err
	}

	podCpus := classifyByTag(cpuResp.GetSeries(), "pod_name", "unknown-pod")
	podMems := classifyByTag(memResp.GetSeries(), "pod_name", "unknown-pod")

	podItems := make([]v1beta1.PodMetrics, 0, len(podCpus))
	for podname, cpuVals := range podCpus {
		podMets := d.aggregatePodMetrics(podname, cpuVals, podMems[podname])
		if podMets != nil {
			podItems = append(podItems, *podMets)
		}
	}

	return &v1beta1.PodMetricsList{Items: podItems}, nil
}

func newDatadogClientWithFactory(queryInterval time.Duration, cluster string, clientApiSecrets string, extraTags []string, newApiClient func(*datadog.Configuration) baseClient) resourceclient.PodMetricsesGetter {
	authData := map[string]string{}
	authDataJson, err := ioutil.ReadFile(clientApiSecrets)
	if err != nil {
		klog.Fatalf("%s: %v", clientApiSecrets, err)
	}

	err = json.Unmarshal(authDataJson, &authData)
	if err != nil {
		klog.Errorf("Failed to parse %v", string(authDataJson))
		klog.Fatalf("%s: %v", clientApiSecrets, err)
	}

	apiKey, apiOk := authData["apiKeyAuth"]
	appKey, appOk := authData["appKeyAuth"]

	if !apiOk || !appOk || len(apiKey) < 1 || len(appKey) < 1 {
		klog.Fatalf("Both apiKeyAuth and appKeyAuth must be set in %s and be valid app/api keys", clientApiSecrets)
		return nil
	}

	if queryInterval.Seconds() < 1 {
		klog.Errorf("Interval has to be at least 1 second.")
		return nil
	}
	if len(cluster) < 1 {
		klog.Errorf("Cluster must be specified.")
		return nil
	}
	ctx := context.WithValue(
		context.Background(),
		datadog.ContextAPIKeys,
		map[string]datadog.APIKey{
			"apiKeyAuth": {
				Key: apiKey,
			},
			"appKeyAuth": {
				Key: appKey,
			},
		},
	)
	site, siteOk := authData["dd_url"]
	if siteOk {
		// Copied shamelessly from github.com/DataDog/disruption-budge-manager/pkg/ddclient/ddclient.go
		datadogAPIURL, err := url.ParseRequestURI(site)
		if err != nil {
			return nil
		}
		// the configured datadgog url on our clusters may be configured to start with the app prefix
		// however the `site` parameter does not support that (it prepends an api prefix), so trim the app prefix if present
		// see https://docs.google.com/document/d/1dqd7X8iTqMw3e61C7ucfZ22Uf22_oAv8qDxdhbDKb58/edit#heading=h.ipmfddl3vahq for more information
		site = datadogAPIURL.Host
		if strings.HasPrefix(site, appURLPrefix) {
			site = site[len(appURLPrefix):]
		}
		ctx = context.WithValue(ctx,
			datadog.ContextServerVariables,
			map[string]string{
				"site": site,
			})
	} else if site, siteOk = authData["site"]; siteOk {
		ctx = context.WithValue(ctx,
			datadog.ContextServerVariables,
			map[string]string{
				"site": site,
			})
	}

	klog.V(2).Infof("NewDatadogClient(%v, %s, site=%s)", queryInterval, cluster, site)
	configuration := datadog.NewConfiguration()
	apiClient := newApiClient(configuration)

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
	return ddclientMetrics{Context: ctx, Client: apiClient, QueryInterval: -queryInterval, ClusterName: cluster, ExtraTagsClause: clause}
}

type clientWrapper struct {
	baseClient
	ApiClient datadog.APIClient
}

func (c clientWrapper) QueryMetrics(context context.Context, interval time.Duration, query string) (datadog.MetricsQueryResponse, *http.Response, error) {
	resp, http, err := c.ApiClient.MetricsApi.QueryMetrics(context, time.Now().Add(interval).Unix(), time.Now().Unix(), query)
	headers := http.Header
	// http.Header is a map[string][]string
	for k, vs := range headers {
		if strings.HasPrefix(strings.ToLower(k), "x-ratelimit") {
			klog.V(2).Infof("Query header: %s, %v", k, vs)
		}
	}
	return resp, http, err
}

func NewDatadogClient(queryInterval time.Duration, cluster string, clientApiSecrets string, extraTags []string) resourceclient.PodMetricsesGetter {
	var wrapFn = func(configuration *datadog.Configuration) baseClient {
		return &clientWrapper{ApiClient: *datadog.NewAPIClient(configuration)}
	}
	return newDatadogClientWithFactory(queryInterval, cluster, clientApiSecrets, extraTags, wrapFn)
}
