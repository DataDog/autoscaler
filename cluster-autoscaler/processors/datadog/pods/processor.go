package pods

import (
	"time"

	"k8s.io/autoscaler/cluster-autoscaler/context"
	"k8s.io/autoscaler/cluster-autoscaler/metrics"
	proc "k8s.io/autoscaler/cluster-autoscaler/processors/pods"

	apiv1 "k8s.io/api/core/v1"
	klog "k8s.io/klog/v2"
)

type filteringPodListProcessor struct {
	transforms []proc.PodListProcessor
	filters    []proc.PodListProcessor
}

func NewFilteringPodListProcessor() *filteringPodListProcessor {
	return &filteringPodListProcessor{
		transforms: []proc.PodListProcessor{
			NewTransformLocalData(),
		},
		filters: []proc.PodListProcessor{
			NewFilterOutLongPending(),
			NewFilterOutSchedulable(),
		},
	}
}

func (p *filteringPodListProcessor) CleanUp() {}

func (p *filteringPodListProcessor) Process(ctx *context.AutoscalingContext, pending []*apiv1.Pod) ([]*apiv1.Pod, error) {
	klog.V(4).Infof("Filtering pending pods")
	start := time.Now()

	var err error

	for _, transform := range p.transforms {
		pending, err = transform.Process(ctx, pending)
		if err != nil {
			return nil, err
		}
	}

	unschedulablePodsToHelp := make([]*apiv1.Pod, len(pending))
	copy(unschedulablePodsToHelp, pending)
	for _, filter := range p.filters {
		unschedulablePodsToHelp, err = filter.Process(ctx, unschedulablePodsToHelp)
		if err != nil {
			return nil, err
		}
	}

	metrics.UpdateDurationFromStart(metrics.FilterOutSchedulable, start)
	return unschedulablePodsToHelp, nil
}
