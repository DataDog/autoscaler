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
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
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

	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
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

func createPodMetrics(namespace, podName string, metricsPerResource map[model.ResourceName][]datadog.MetricsQueryMetadata) *v1beta1.PodMetrics {
	if namespace == "" {
		allMetrics := [][]datadog.MetricsQueryMetadata{}
		for _, m := range metricsPerResource {
			allMetrics = append(allMetrics, m)
		}
		var found bool
		namespace, found = extractNamespaceFromMetrics(allMetrics...)
		if !found {
			klog.Errorf("Never found the namespace for podName=%s", podName)
			return nil
		}
	}

	containerMetrics := map[string]v1beta1.ContainerMetrics{}
	for resourceName, serie := range metricsPerResource {
		perContainer := classifyByTag(serie, "container_name", "unknown-container")
		for containerName, ress := range perContainer {
			var maxValue float64
			var maxQuantity resource.Quantity
			var valueIsSet bool
			for _, res := range ress {
				for rowIdx, row := range res.Pointlist {
					if len(row) > 1 && row[0] != nil && row[1] != nil {
						if *row[1] >= maxValue { // >= and not > to capture the 0 values also
							maxValue = *row[1]
							maxQuantity = ddMetricsToQuantityFuncs[resourceName](res, maxValue)
							valueIsSet = true
						}
					} else {
						klog.V(1).Infof("Got short row for container %v: row[%d]=%v",
							containerName, rowIdx, row)
					}
				}
			}
			if valueIsSet {
				cm := containerMetrics[containerName]
				if cm.Name == "" {
					cm.Name = containerName
					cm.Usage = map[v1.ResourceName]resource.Quantity{}
				}
				cm.Usage[v1.ResourceName(resourceName)] = maxQuantity
				containerMetrics[containerName] = cm
			} else {
				klog.V(1).Infof("Resource %s not set for container %s/%s/%s", resourceName, namespace, podName, containerName)
			}
		}
	}
	containerMetricsSlice := make([]v1beta1.ContainerMetrics, len(containerMetrics))
	i := 0
	for _, cm := range containerMetrics {
		containerMetricsSlice[i] = cm
		i++
	}

	return &v1beta1.PodMetrics{
		ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: namespace, UID: types.UID(namespace + "." + podName)},
		Containers: containerMetricsSlice}
}

func extractNamespaceFromMetrics(ddMetrics ...[]datadog.MetricsQueryMetadata) (string, bool) {
	namespace, ok := findFirstTagOccurrence("kube_namespace", ddMetrics...)
	if !ok {
		klog.Errorf("Can't extract kube_namespace from series")
		return "", false
	}
	return namespace, true
}

type transformDDMetricFunc func(datadog.MetricsQueryMetadata, float64) resource.Quantity

var ddMetricsToQuantityFuncs = map[model.ResourceName]transformDDMetricFunc{
	model.ResourceCPU:    ddMetricsToMilliQuantity,
	model.ResourceMemory: ddMetricsToQuantity,
}

func ddMetricsToMilliQuantity(met datadog.MetricsQueryMetadata, value float64) resource.Quantity {
	return *resource.NewMilliQuantity(int64(scaleCpuToCores(met, value)*1000.0), resource.DecimalSI)
}
func ddMetricsToQuantity(met datadog.MetricsQueryMetadata, value float64) resource.Quantity {
	return *resource.NewQuantity(int64(scaleMemToBytes(met, value)), resource.DecimalSI)
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

const (
	timeShift = 2 * time.Minute // ensure that we fetch a bit in the past to let time to the intake to digest values
)

func (d *ddclientPodMetrics) Get(ctx context.Context, podName string, _ metav1.GetOptions) (*v1beta1.PodMetrics, error) {
	nsClause := ""
	if len(d.Namespace) > 0 {
		nsClause = fmt.Sprintf(" AND kube_namespace:%s ", d.Namespace)
	}

	end := time.Now().Add(-timeShift)
	start := end.Add(-d.QueryInterval)

	rollupSeconds := int64(math.Floor(d.QueryInterval.Seconds() / 2))
	if rollupSeconds == 0 {
		rollupSeconds = 1
	}
	cpuResp, err := d.queryMetrics(ctx, start, end, fmt.Sprintf("max:kubernetes.cpu.usage.total{kube_cluster_name:%s%s%s AND pod_name:%s}by{kube_namespace,container_name}.rollup(max, %d)",
		d.ClusterName, nsClause, d.ExtraTagsClause, podName, rollupSeconds))
	if err != nil {
		klog.V(1).Infof("Query cpu error, %s", err)
		klog.Errorf(err.Error())
		return nil, err
	}
	memResp, err := d.queryMetrics(ctx, start, end, fmt.Sprintf("max:kubernetes.memory.usage{ kube_cluster_name:%s%s%s AND pod_name:%s}by{kube_namespace,container_name}.rollup(max, %d)",
		d.ClusterName, nsClause, d.ExtraTagsClause, podName, rollupSeconds))
	if err != nil {
		klog.V(1).Infof("Query mem error, %s", err)
		klog.Errorf(err.Error())
		return nil, err
	}

	metricsPerResource := map[model.ResourceName][]datadog.MetricsQueryMetadata{
		model.ResourceCPU:    cpuResp.GetSeries(),
		model.ResourceMemory: memResp.GetSeries(),
	}
	podMetrics := createPodMetrics(d.Namespace, podName, metricsPerResource)
	podMetrics.Timestamp = metav1.Time{Time: start}
	podMetrics.Window = metav1.Duration{Duration: d.QueryInterval}

	return podMetrics, nil
}

func (d *ddclientPodMetrics) List(ctx context.Context, _ metav1.ListOptions) (*v1beta1.PodMetricsList, error) {
	nsClause := ""
	if len(d.Namespace) > 0 {
		nsClause = fmt.Sprintf(" AND kube_namespace:%s ", d.Namespace)
	}

	end := time.Now().Add(-timeShift)
	start := end.Add(-d.QueryInterval)

	rollupSeconds := int64(math.Floor(d.QueryInterval.Seconds()))
	if rollupSeconds == 0 {
		rollupSeconds = 1
	}
	cpuResp, err := d.queryMetrics(ctx, start, end, fmt.Sprintf("max:kubernetes.cpu.usage.total{kube_cluster_name:%s%s%s}by{kube_namespace,pod_name,container_name}.rollup(max, %d)",
		d.ClusterName, nsClause, d.ExtraTagsClause, rollupSeconds))
	if err != nil {
		klog.V(1).Infof("Query cpu error, %s", err)
		klog.Errorf(err.Error())
		return nil, err
	}
	memResp, err := d.queryMetrics(ctx, start, end, fmt.Sprintf("max:kubernetes.memory.usage{ kube_cluster_name:%s%s%s}by{kube_namespace,pod_name,container_name}.rollup(max, %d)",
		d.ClusterName, nsClause, d.ExtraTagsClause, rollupSeconds))
	if err != nil {
		klog.V(1).Infof("Query mem error, %s", err)
		klog.Errorf(err.Error())
		return nil, err
	}

	pml := &v1beta1.PodMetricsList{}
	podCpus := classifyByTag(cpuResp.GetSeries(), "pod_name", "unknown-pod")
	podMems := classifyByTag(memResp.GetSeries(), "pod_name", "unknown-pod")

	podNames := map[string]struct{}{}
	for podName := range podCpus {
		podNames[podName] = struct{}{}
	}
	for podName := range podMems {
		podNames[podName] = struct{}{}
	}
	for podName := range podNames {
		metricsPerResource := map[model.ResourceName][]datadog.MetricsQueryMetadata{}
		if sCPU, ok := podCpus[podName]; ok {
			metricsPerResource[model.ResourceCPU] = sCPU
		}
		if sMem, ok := podMems[podName]; ok {
			metricsPerResource[model.ResourceMemory] = sMem
		}

		podMetrics := createPodMetrics(d.Namespace, podName, metricsPerResource)
		if podMetrics == nil {
			continue
		}
		podMetrics.Timestamp = metav1.Time{Time: start}
		podMetrics.Window = metav1.Duration{Duration: d.QueryInterval}
		pml.Items = append(pml.Items, *podMetrics)
	}

	// TODO there is a potential issue here as we are losing the namespace! STS pod can have the same name in different namespaces!
	return pml, err
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
	reqTime := time.Now()
	resp, httpResponse, err := c.ApiClient.MetricsApi.QueryMetrics(ctx, start.Unix(), end.Unix(), query)
	respTime := time.Now()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error when calling `MetricsApi.QueryMetrics` on %s: %v\n", query, err)
		fmt.Fprintf(os.Stderr, "Full HTTP response: %v\n", httpResponse)
		return datadog.MetricsQueryResponse{}, err
	}
	b, _ := json.Marshal(resp)
	klog.V(1).Infof("queryMetrics('%s'): got response[%s seconds, %d bytes] with %d series from %d to %d",
		query,
		strconv.FormatFloat(respTime.Sub(reqTime).Seconds(), 'f', 3, 64),
		len(b),
		len(resp.GetSeries()), resp.GetFromDate(), resp.GetToDate())

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
