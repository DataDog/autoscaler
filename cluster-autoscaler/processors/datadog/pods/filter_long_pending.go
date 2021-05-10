/*
  Ensures pods pending for a long time are retried at a slower pace:
  * We don't want those long pending to slow down scale-up for fresh pods.
  * If one of those is a pod causing a runaway infinite upscale, we want to give
    autoscaler some slack time to recover from cooldown, and reap unused nodes.
*/
package pods

import (
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/autoscaler/cluster-autoscaler/context"

	apiv1 "k8s.io/api/core/v1"
	klog "k8s.io/klog/v2"
)

const maxDistinctWorkloadsHavingPendingPods = 30

var now = time.Now // unit tests

type pendingTracker struct {
	firstSeen time.Time
	lastTried time.Time
}

type filterOutLongPending struct {
	seen map[types.UID]*pendingTracker
}

func NewFilterOutLongPending() *filterOutLongPending {
	return &filterOutLongPending{
		seen: make(map[types.UID]*pendingTracker),
	}
}

func (p *filterOutLongPending) CleanUp() {}

func (p *filterOutLongPending) Process(ctx *context.AutoscalingContext, pending []*apiv1.Pod) ([]*apiv1.Pod, error) {
	longPendingBackoff := ctx.AutoscalingOptions.ScaleDownDelayAfterAdd * 2

	currentPods := make(map[types.UID]struct{}, len(pending))
	allowedPods := make([]*apiv1.Pod, 0)

	if countDistinctOwnerReferences(pending) > maxDistinctWorkloadsHavingPendingPods {
		klog.Warning("detected pending pods from many distinct workloads:" +
			" disabling backoff on long pending pods")
		return pending, nil
	}

	for _, pod := range pending {
		currentPods[pod.UID] = struct{}{}
		if _, found := p.seen[pod.UID]; !found {
			p.seen[pod.UID] = &pendingTracker{
				firstSeen: now(),
				lastTried: now(),
			}
		}

		if p.seen[pod.UID].firstSeen.Add(longPendingCutoff).Before(now()) {
			deadline := p.seen[pod.UID].lastTried.Add(longPendingBackoff)
			if deadline.After(now()) {
				klog.Warningf("ignoring long pending pod %s/%s until %s",
					pod.GetNamespace(), pod.GetName(), deadline)
				continue
			}
		}
		p.seen[pod.UID].lastTried = now()

		allowedPods = append(allowedPods, pod)
	}

	for uid := range p.seen {
		if _, found := currentPods[uid]; !found {
			delete(p.seen, uid)
		}
	}

	return allowedPods, nil
}
