package pods

import (
	"testing"

	"github.com/stretchr/testify/assert"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestCountDistinctOwnerReferences(t *testing.T) {
	tests := []struct {
		name     string
		pods     []*apiv1.Pod
		expected int
	}{
		{
			"count all distinct ownerref",
			[]*apiv1.Pod{testPodWithOwner("a"), testPodWithOwner("b"), testPodWithOwner("c")},
			3,
		},

		{
			"group identical ownerrefs",
			[]*apiv1.Pod{testPodWithOwner("a"), testPodWithOwner("a"), testPodWithOwner("b")},
			2,
		},

		{
			"don't crash on empty pod list",
			[]*apiv1.Pod{},
			0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := countDistinctOwnerReferences(tt.pods)
			assert.Equal(t, actual, tt.expected)
		})
	}
}

func testPodWithOwner(refname string) *apiv1.Pod {
	trueish := true
	return &apiv1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			OwnerReferences: []metav1.OwnerReference{{
				UID:        types.UID(refname),
				Name:       refname,
				Controller: &trueish,
			}},
		},
	}
}
