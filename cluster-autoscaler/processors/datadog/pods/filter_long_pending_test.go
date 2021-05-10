package pods

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/autoscaler/cluster-autoscaler/config"
	"k8s.io/autoscaler/cluster-autoscaler/context"
)

var testScaleDownDelay = time.Minute

var testCtx = &context.AutoscalingContext{
	AutoscalingOptions: config.AutoscalingOptions{
		ScaleDownDelayAfterAdd: testScaleDownDelay,
	},
}

func TestFilterOutLongPending(t *testing.T) {
	set1 := buildPendingPods(5, "a")
	set2 := buildPendingPods(2, "b")
	set1and2 := append(set1, set2...)
	largeset := buildPendingPods(2*maxDistinctWorkloadsHavingPendingPods, "c")

	tests := []struct {
		name            string
		podsInFirstCall []*apiv1.Pod
		podsInNextCall  []*apiv1.Pod
		firstCallDelay  time.Duration
		nextCallDelay   time.Duration
		expected        []*apiv1.Pod
	}{
		{"none filtered when no long pending pods", set1, set1and2, time.Minute, testScaleDownDelay, set1and2},
		{"long pending pods are filtered out", set1, set1and2, 2 * longPendingCutoff, testScaleDownDelay / 2, set2},
		{"retry long pending after some time", set1, set1and2, 2 * longPendingCutoff, testScaleDownDelay * 2, set1and2},
		{"circuit-break and on huge backlog", largeset, largeset, 2 * longPendingCutoff, testScaleDownDelay / 2, largeset},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now = time.Now
			fp := NewFilterOutLongPending()

			actual, err := fp.Process(testCtx, tt.podsInFirstCall)
			assert.NoError(t, err)

			now = func() time.Time { return time.Now().Add(tt.firstCallDelay) }
			actual, err = fp.Process(testCtx, tt.podsInNextCall)
			assert.ElementsMatch(t, actual, tt.podsInNextCall, "unexpected pods filtered out")
			assert.NoError(t, err)

			now = func() time.Time { return time.Now().Add(tt.firstCallDelay).Add(tt.nextCallDelay) }
			actual, err = fp.Process(testCtx, tt.podsInNextCall)
			assert.ElementsMatch(t, actual, tt.expected, "unexpected pods filtered out")
			assert.NoError(t, err)
		})
	}
}

func buildPendingPods(count int, setName string) []*apiv1.Pod {
	var result []*apiv1.Pod
	trueish := true
	for i := 0; i < count; i++ {
		result = append(result, &apiv1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				UID: types.UID(fmt.Sprintf("%s-%d", setName, i)),
				OwnerReferences: []metav1.OwnerReference{{
					UID:        types.UID(fmt.Sprintf("%s-%d", setName, i)),
					Name:       fmt.Sprintf("%s-%d", setName, i),
					Controller: &trueish,
				}},
			},
		})
	}
	return result
}
