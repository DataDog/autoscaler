package metrics

import (
	"context"
	k8sapiv1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	externalmetricsv1beta1 "k8s.io/metrics/pkg/apis/external_metrics/v1beta1"
	"k8s.io/metrics/pkg/apis/metrics/v1beta1"
	"time"

	resourceclient "k8s.io/metrics/pkg/client/clientset/versioned/typed/metrics/v1beta1"
	"k8s.io/metrics/pkg/client/external_metrics"
)

type Source interface {
	List(ctx context.Context, namespace string, model *model.ClusterState, opts v1.ListOptions) (*v1beta1.PodMetricsList, error)
}

type podMetricsSource struct {
	metricsGetter resourceclient.PodMetricsesGetter
}

func NewPodMetricsesSource(source resourceclient.PodMetricsesGetter) Source {
	return podMetricsSource{metricsGetter: source}
}

func (s podMetricsSource) List(ctx context.Context, namespace string, _ *model.ClusterState, opts v1.ListOptions) (*v1beta1.PodMetricsList, error) {
	podMetricsInterface := s.metricsGetter.PodMetricses(namespace)
	return podMetricsInterface.List(ctx, opts)
}

type externalMetricsClient struct {
	externalClient external_metrics.ExternalMetricsClient
	options        ExternalClientOptions
}

type ExternalClientOptions struct {
	CpuMetric, MemoryMetric                          string
	PodNamespaceLabel, PodNameLabel                  string
	CtrNamespaceLabel, CtrPodNameLabel, CtrNameLabel string
}

func NewExternalClient(c *rest.Config, options ExternalClientOptions) Source {
	extClient, err := external_metrics.NewForConfig(c)
	if err != nil {
		klog.Fatalf("Failed initializing external metrics client: %v", err)
	}
	return externalMetricsClient{
		externalClient: extClient,
		options:        options,
	}
}

func (s externalMetricsClient) containerId(value externalmetricsv1beta1.ExternalMetricValue) *model.ContainerID {
	podNS, hasPodNS := value.MetricLabels[s.options.PodNamespaceLabel]
	podName, hasPodName := value.MetricLabels[s.options.PodNameLabel]
	ctrName, hasCtrName := value.MetricLabels[s.options.CtrNameLabel]
	if hasPodNS && hasPodName && hasCtrName {
		return &model.ContainerID{
			PodID:         model.PodID{Namespace: podNS, PodName: podName},
			ContainerName: ctrName,
		}
	} else {
		return nil
	}
}

type podContainerResourceMap map[model.PodID]map[string]k8sapiv1.ResourceList

func (s externalMetricsClient) addMetrics(list *externalmetricsv1beta1.ExternalMetricValueList, name k8sapiv1.ResourceName, resourceMap *podContainerResourceMap) {
	for _, val := range list.Items {
		if id := s.containerId(val); id != nil {
			(*resourceMap)[id.PodID][id.ContainerName][name] = val.Value
		}
	}
}

func (s externalMetricsClient) List(ctx context.Context, namespace string, state *model.ClusterState, opts v1.ListOptions) (*v1beta1.PodMetricsList, error) {
	result := v1beta1.PodMetricsList{}
	// Get all VPAs in the namespace
	// - We already do this in the cluster state feeder!  It's in its clusterState member.
	//   We just have to feed it into here somehow.
	// - use the 'PodSelector' there as the input to the external api.
	// Send out the queries.
	nsClient := s.externalClient.NamespacedMetrics(namespace)

	for _, vpa := range state.Vpas {
		if vpa.PodCount == 0 {
			continue
		}
		workloadValues := make(podContainerResourceMap)
		cpuMetrics, err := nsClient.List(s.options.CpuMetric, vpa.PodSelector)
		if err != nil {
			return nil, err
		}
		memMetrics, err := nsClient.List(s.options.MemoryMetric, vpa.PodSelector)
		if err != nil {
			return nil, err
		}

		if cpuMetrics == nil || len(cpuMetrics.Items) == 0 || memMetrics == nil || len(memMetrics.Items) == 0 {
			continue
		}

		s.addMetrics(cpuMetrics, k8sapiv1.ResourceCPU, &workloadValues)
		s.addMetrics(memMetrics, k8sapiv1.ResourceMemory, &workloadValues)

		for podId, cmaps := range workloadValues {
			podMets := v1beta1.PodMetrics{
				TypeMeta:   v1.TypeMeta{},
				ObjectMeta: v1.ObjectMeta{Name: podId.PodName, Namespace: podId.Namespace},
				Timestamp:  cpuMetrics.Items[0].Timestamp,
				Window:     v1.Duration{Duration: time.Second * time.Duration(*cpuMetrics.Items[0].WindowSeconds)},
				Containers: make([]v1beta1.ContainerMetrics, 0, len(cmaps)),
			}
			for cname, res := range cmaps {
				podMets.Containers = append(podMets.Containers, v1beta1.ContainerMetrics{Name: cname, Usage: res})
			}
			result.Items = append(result.Items, podMets)
		}
	}
	return &result, nil
}
