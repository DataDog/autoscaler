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

package main

import (
	"flag"
	"time"

	apiv1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	kube_client "k8s.io/client-go/kubernetes"
	kube_flag "k8s.io/component-base/cli/flag"
	"k8s.io/klog/v2"

	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender-external/routines"
	controllerfetcher "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/input/controller_fetcher"
	metrics_quality "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/metrics/quality"
	metrics_recommender "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/metrics/recommender"

	"k8s.io/autoscaler/vertical-pod-autoscaler/common"
	upstream_routines "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/routines"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/metrics"
)

var (
	recommenderName        = flag.String("recommender-name", routines.DefaultRecommenderName, "Set the recommender name. ExternalRecommender will generate recommendations for VPAs that configure the same recommender name. If the recommender name is left as default it will also generate recommendations that don't explicitly specify recommender. You shouldn't run two recommenders with the same name in a cluster.")
	metricsFetcherInterval = flag.Duration("recommender-interval", 1*time.Minute, `How often metrics should be fetched`)
	address                = flag.String("address", ":8942", "The address to expose Prometheus metrics.")
	kubeconfig             = flag.String("kubeconfig", "", "Path to a kubeconfig. Only required if out-of-cluster.")
	kubeApiQps             = flag.Float64("kube-api-qps", 5.0, `QPS limit when making requests to Kubernetes apiserver`)
	kubeApiBurst           = flag.Float64("kube-api-burst", 10.0, `QPS burst limit when making requests to Kubernetes apiserver`)

	vpaObjectNamespace = flag.String("vpa-object-namespace", apiv1.NamespaceAll, "Namespace to search for VPA objects and pod stats. Empty means all namespaces will be used.")

	// TODO:
	// - API and APP Keys (default to env var, check David's PR)
)

// Post processors flags
var (
	// CPU as integer to benefit for CPU management Static Policy ( https://kubernetes.io/docs/tasks/administer-cluster/cpu-management-policies/#static-policy )
	postProcessorCPUasInteger = flag.Bool("cpu-integer-post-processor-enabled", false, "Enable the cpu-integer recommendation post processor. The post processor will round up CPU recommendations to a whole CPU for pods which were opted in by setting an appropriate label on VPA object (experimental)")
	// Resource ratio post-processor. Ability to ensure that resource ratio is maintained. For example CPU recommendation is done as usual, and the Memory recommendation is computed so that the initial ratio between memory and CPU is maintained. This could be useful for garbage collected languages where the runtime tries to release memory when consumption is closed to the limit, resulting in degraded performances.
	postProcessorMaintainResourceRatio = flag.Bool("resource-ratio-post-processor-enabled", false, "Enable the resource-ratio recommendation post processor. The post processor will ensure that resource ratio is maintain for pods which were opted in by setting an appropriate label on VPA object (experimental)")
	// Replica restrictions post-processor.
	postProcessorReplicaRestrictions = flag.Bool("replica-restrictions-post-processor-enabled", false, "Enable the replica-restrictions recommendation post processor. The post processor will ensure that the allow upscales/downscales based on the currently running number of replicas for controllers which were opted in by setting an appropriate label on VPA object (experimental)")
)

const (
	scaleCacheEntryLifetime      time.Duration = time.Hour
	scaleCacheEntryFreshnessTime time.Duration = 10 * time.Minute
	scaleCacheEntryJitterFactor  float64       = 1.
	defaultResyncPeriod          time.Duration = 10 * time.Minute
)

func main() {
	klog.InitFlags(nil)
	kube_flag.InitFlags()
	klog.V(1).Infof("Vertical Pod Autoscaler %s Datadog ExternalRecommender: %v", common.VerticalPodAutoscalerVersion, recommenderName)

	config := common.CreateKubeConfigOrDie(*kubeconfig, float32(*kubeApiQps), int(*kubeApiBurst))
	kubeClient := kube_client.NewForConfigOrDie(config)
	factory := informers.NewSharedInformerFactoryWithOptions(kubeClient, defaultResyncPeriod, informers.WithNamespace(*vpaObjectNamespace))
	controllerFetcher := controllerfetcher.NewControllerFetcher(config, kubeClient, factory, scaleCacheEntryFreshnessTime, scaleCacheEntryLifetime, scaleCacheEntryJitterFactor)

	healthCheck := metrics.NewHealthCheck(*metricsFetcherInterval*5, true)
	metrics.Initialize(*address, healthCheck)
	metrics_recommender.Register()
	metrics_quality.Register()

	var postProcessors []upstream_routines.RecommendationPostProcessor
	if *postProcessorCPUasInteger {
		postProcessors = append(postProcessors, &upstream_routines.IntegerCPUPostProcessor{})
	}
	if *postProcessorMaintainResourceRatio {
		postProcessors = append(postProcessors, &upstream_routines.ReplicaRestrictionsPostProcessor{ControllerFetcher: controllerFetcher})
	}
	if *postProcessorReplicaRestrictions {
		postProcessors = append(postProcessors, &upstream_routines.ReplicaRestrictionsPostProcessor{ControllerFetcher: controllerFetcher})
	}
	// CappingPostProcessor, should always come in the last position for post-processing
	postProcessors = append(postProcessors, &upstream_routines.CappingPostProcessor{})

	recommender := routines.NewExternalRecommender(config, *vpaObjectNamespace, *recommenderName, postProcessors)

	ticker := time.Tick(*metricsFetcherInterval)
	for range ticker {
		recommender.RunOnce()
		healthCheck.UpdateLastActivity()
	}
}
