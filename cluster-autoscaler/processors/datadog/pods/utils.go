package pods

import (
	"time"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type AgeCondition int

const (
	longPendingCutoff = time.Hour * 2
	YoungerThan       = iota
	OlderThan         = iota
)

func countDistinctOwnerReferences(pods []*apiv1.Pod) int {
	distinctOwners := make(map[types.UID]struct{})
	for _, pod := range pods {
		controllerRef := metav1.GetControllerOf(pod)
		if controllerRef == nil {
			continue
		}
		distinctOwners[controllerRef.UID] = struct{}{}
	}

	return len(distinctOwners)
}

func filterByAge(pods []*apiv1.Pod, condition AgeCondition, age time.Duration) []*apiv1.Pod {
	var filtered []*apiv1.Pod
	for _, pod := range pods {
		cutoff := pod.GetCreationTimestamp().Time.Add(age)
		if condition == YoungerThan && cutoff.After(time.Now()) {
			filtered = append(filtered, pod)
		}
		if condition == OlderThan && cutoff.Before(time.Now()) {
			filtered = append(filtered, pod)
		}
	}
	return filtered
}
