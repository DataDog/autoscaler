/*
Copyright 2016 The Kubernetes Authors.

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

package drain

import (
	"fmt"
	"testing"
	"time"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIsPodLongTerminating(t *testing.T) {
	testTime := time.Date(2020, time.December, 18, 17, 0, 0, 0, time.UTC)
	twoMinGracePeriod := int64(2 * 60)

	tests := []struct {
		name string
		pod  apiv1.Pod
		want bool
	}{
		{
			name: "No deletion timestamp",
			pod: apiv1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: nil,
				},
			},
			want: false,
		},
		{
			name: "Deletion timestamp in the future",
			pod: apiv1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &metav1.Time{Time: testTime.Add(time.Minute)},
				},
				Spec: apiv1.PodSpec{
					TerminationGracePeriodSeconds: &twoMinGracePeriod,
				},
			},
			want: false,
		},
		{
			name: "Deletion timestamp passed but not longer than PodLongTerminatingExtraThreshold",
			pod: apiv1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &metav1.Time{Time: testTime.Add(-PodLongTerminatingExtraThreshold / 2)},
				},
			},
			want: false,
		},
		{
			name: "Deletion timestamp passed for longer than PodLongTerminatingExtraThreshold",
			pod: apiv1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &metav1.Time{Time: testTime.Add(-2 * PodLongTerminatingExtraThreshold)},
				},
			},
			want: true,
		},
		{
			name: "Deletion timestamp passed for longer than PodLongTerminatingExtraThreshold but not longer than grace period (2 min)",
			pod: apiv1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &metav1.Time{Time: testTime.Add(-2 * PodLongTerminatingExtraThreshold)},
				},
				Spec: apiv1.PodSpec{
					TerminationGracePeriodSeconds: &twoMinGracePeriod,
				},
			},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsPodLongTerminating(&tc.pod, testTime); got != tc.want {
				t.Errorf("IsPodLongTerminating() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBlockingPodReasonFormatting(t *testing.T) {
	for _, tc := range []struct {
		bpr  BlockingPodReason
		want string
	}{
		{
			bpr:  NoReason,
			want: "NoReason",
		},
		{
			bpr:  ControllerNotFound,
			want: "ControllerNotFound",
		},
		{
			bpr:  ControllerNotFound,
			want: "ControllerNotFound",
		},
		{
			bpr:  NotReplicated,
			want: "NotReplicated",
		},
		{
			bpr:  LocalStorageRequested,
			want: "LocalStorageRequested",
		},
		{
			bpr:  NotSafeToEvictAnnotation,
			want: "NotSafeToEvictAnnotation",
		},
		{
			bpr:  UnmovableKubeSystemPod,
			want: "UnmovableKubeSystemPod",
		},
		{
			bpr:  NotEnoughPdb,
			want: "NotEnoughPdb",
		},
		{
			bpr:  UnexpectedError,
			want: "UnexpectedError",
		},
		{
			bpr:  BlockingPodReason(9),
			want: "unrecognized reason: 9",
		},
	} {
		t.Run(tc.want, func(t *testing.T) {
			if got := fmt.Sprintf("%v", tc.bpr); got != tc.want {
				t.Errorf("got: %s, want %s", got, tc.want)
			}
		})
	}
}
