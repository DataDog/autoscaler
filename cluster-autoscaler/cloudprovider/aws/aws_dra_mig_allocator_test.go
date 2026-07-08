/*
Copyright The Kubernetes Authors.

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

package aws

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/dynamic-resource-allocation/cel"
	"k8s.io/dynamic-resource-allocation/structured"
)

// fakeDeviceClassLister is a minimal structured.DeviceClassLister for tests that only
// need a fixed, in-memory set of DeviceClasses.
type fakeDeviceClassLister struct {
	classes map[string]*resourceapi.DeviceClass
}

func (f *fakeDeviceClassLister) List() ([]*resourceapi.DeviceClass, error) {
	classes := make([]*resourceapi.DeviceClass, 0, len(f.classes))
	for _, c := range f.classes {
		classes = append(classes, c)
	}
	return classes, nil
}

func (f *fakeDeviceClassLister) Get(className string) (*resourceapi.DeviceClass, error) {
	class, ok := f.classes[className]
	if !ok {
		return nil, fmt.Errorf("class %s not found", className)
	}
	return class, nil
}

func migDeviceClass() *resourceapi.DeviceClass {
	return &resourceapi.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{Name: "mig.nvidia.com"},
		Spec: resourceapi.DeviceClassSpec{
			Selectors: []resourceapi.DeviceSelector{{
				CEL: &resourceapi.CELDeviceSelector{
					Expression: `device.driver == "gpu.nvidia.com" && device.attributes["gpu.nvidia.com"].type == "mig"`,
				},
			}},
		},
	}
}

func migClaim(name, profile string) *resourceapi.ResourceClaim {
	return &resourceapi.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test", UID: types.UID(name)},
		Spec: resourceapi.ResourceClaimSpec{
			Devices: resourceapi.DeviceClaim{
				Requests: []resourceapi.DeviceRequest{{
					Name: "mig-request",
					Exactly: &resourceapi.ExactDeviceRequest{
						DeviceClassName: "mig.nvidia.com",
						AllocationMode:  resourceapi.DeviceAllocationModeExactCount,
						Count:           1,
						Selectors: []resourceapi.DeviceSelector{{
							CEL: &resourceapi.CELDeviceSelector{
								Expression: fmt.Sprintf(`device.attributes["gpu.nvidia.com"].profile == "%s"`, profile),
							},
						}},
					},
				}},
			},
		},
	}
}

// TestMIGCounterConflictDetection exercises the real k8s.io/dynamic-resource-allocation
// structured allocator (not our fabrication code) against our own fabricated MIG
// ResourceSlices, proving that once a 1g.24gb partition is allocated, a conflicting
// 4g.96gb (whole-GPU) claim requesting overlapping memory slices is correctly rejected.
//
// This is a regression test for a real upstream bug found while testing DRA scale-up
// on a live cluster: k8s.io/dynamic-resource-allocation v0.34.2 (CA 1.34.x) tracked
// partitionable-device counter consumption per-ResourceSlice instead of per-pool, so it
// never detected this conflict when SharedCounters live in a separate slice from
// Devices — the standard KEP-4815 shape both the real NVIDIA driver and
// buildMIGResourceSlices use. Fixed upstream in v0.35.0 (kubernetes/kubernetes#134189),
// which this branch (CA 1.35) vendors. This test would fail if run against v0.34.2.
func TestMIGCounterConflictDetection(t *testing.T) {
	node := draNode("test-node", map[string]string{
		draDriverLabelKey:     "gpu.nvidia.com",
		draMIGEnabledLabelKey: "true",
	})
	instanceType := &InstanceType{
		InstanceType: "g7e.4xlarge",
		GPU:          1,
		GPUShortName: rtxPro6000ShortName,
	}

	slices := buildMIGResourceSlices(node, instanceType, "gpu.nvidia.com")
	require.NotEmpty(t, slices)

	// Simulate the 1g.24gb partition at placement 0 (gpu-0-mig-1g24gb-14-0) already
	// being allocated to a held claim on the real node.
	heldDevice := structured.MakeDeviceID("gpu.nvidia.com", node.Name, "gpu-0-mig-1g24gb-14-0")
	allocatedState := structured.AllocatedState{
		AllocatedDevices:         sets.New(heldDevice),
		AllocatedSharedDeviceIDs: sets.New[structured.SharedDeviceID](),
		AggregatedCapacity:       structured.NewConsumedCapacityCollection(),
	}

	classLister := &fakeDeviceClassLister{classes: map[string]*resourceapi.DeviceClass{
		"mig.nvidia.com": migDeviceClass(),
	}}

	ctx := context.Background()
	celCache := cel.NewCache(10, cel.Features{})

	allocator, err := structured.NewAllocator(ctx, structured.Features{PartitionableDevices: true}, allocatedState, classLister, slices, celCache)
	require.NoError(t, err)

	conflicting := migClaim("conflicting-claim", "4g.96gb")
	result, err := allocator.Allocate(ctx, node, []*resourceapi.ResourceClaim{conflicting})
	require.NoError(t, err)
	assert.Empty(t, result, "expected 4g.96gb to be rejected: it needs all 12 memory slices, which overlaps the already-allocated 1g.24gb partition's slices 0-2")

	// Sanity check: the allocator isn't just rejecting everything. A non-overlapping
	// 1g.24gb placement (e.g. slices 3-5, at a different offset than the held device)
	// should still be allocatable.
	nonConflicting := migClaim("non-conflicting-claim", "1g.24gb")
	result2, err := allocator.Allocate(ctx, node, []*resourceapi.ResourceClaim{nonConflicting})
	require.NoError(t, err)
	assert.NotEmpty(t, result2, "expected a non-overlapping 1g.24gb placement to still be allocatable")
}
