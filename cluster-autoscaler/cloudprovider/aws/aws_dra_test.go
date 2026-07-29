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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func draNode(name string, labels map[string]string) *apiv1.Node {
	return &apiv1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
	}
}

func TestBuildResourceSlicesFromTemplate_NonDRA(t *testing.T) {
	tests := []struct {
		name         string
		labels       map[string]string
		instanceType *InstanceType
	}{
		{
			name:         "no dra-driver label returns nil",
			labels:       map[string]string{},
			instanceType: &InstanceType{InstanceType: "g5.xlarge", GPU: 1, GPUShortName: "A10G"},
		},
		{
			name:         "dra-driver set but no GPU returns nil",
			labels:       map[string]string{draDriverLabelKey: "gpu.nvidia.com"},
			instanceType: &InstanceType{InstanceType: "m5.xlarge", GPU: 0},
		},
		{
			name:         "nil instance type returns nil",
			labels:       map[string]string{draDriverLabelKey: "gpu.nvidia.com"},
			instanceType: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildResourceSlicesFromTemplate(draNode("node-1", tc.labels), tc.instanceType)
			assert.Nil(t, got)
		})
	}
}

func TestBuildResourceSlicesFromTemplate_FullGPU(t *testing.T) {
	node := draNode("node-1", map[string]string{draDriverLabelKey: "gpu.nvidia.com"})
	// g5.12xlarge has 4 A10G GPUs; the real driver emits one slice per GPU.
	it := &InstanceType{InstanceType: "g5.12xlarge", GPU: 4, GPUShortName: "A10G", GPUMemoryMiB: 24576}

	slices := buildResourceSlicesFromTemplate(node, it)

	// One slice per GPU (verified from live AWS GPU nodes).
	assert.Len(t, slices, 4, "one slice per GPU")
	for i, slice := range slices {
		assert.Equal(t, "gpu.nvidia.com", slice.Spec.Driver)
		assert.Equal(t, "node-1", slice.Spec.Pool.Name)
		if assert.NotNil(t, slice.Spec.NodeName) {
			assert.Equal(t, "node-1", *slice.Spec.NodeName)
		}
		assert.Len(t, slice.Spec.Devices, 1, "one device per slice")
		dev := slice.Spec.Devices[0]
		assert.Equal(t, fmt.Sprintf("gpu-%d", i), dev.Name)
	}

	dev := slices[0].Spec.Devices[0]
	// Rich attributes verified from live AWS g5 nodes.
	assertStringAttr(t, dev, "type", "gpu")
	assertStringAttr(t, dev, "productName", "NVIDIA A10G")
	assertStringAttr(t, dev, "brand", "Nvidia") // driver reports "Nvidia", not "NVIDIA"
	assertStringAttr(t, dev, "architecture", "Ampere")
	if attr, ok := dev.Attributes["cudaComputeCapability"]; assert.True(t, ok) {
		assert.Equal(t, "8.6.0", *attr.VersionValue)
	}
	if cap, ok := dev.Capacity["memory"]; assert.True(t, ok) {
		// 24576Mi canonicalizes to 24Gi (same quantity).
		assert.Equal(t, "24Gi", cap.Value.String())
	}
}

// TestBuildResourceSlicesFromTemplate_FullGPU_MemoryOverride checks that a fullGPUAttrs entry
// with MemoryMiB set overrides EC2's reported GPUMemoryMiB for the full-GPU device capacity —
// the driver's actual published memory can be lower than EC2's nominal figure.
func TestBuildResourceSlicesFromTemplate_FullGPU_MemoryOverride(t *testing.T) {
	saved := gpuDataSource
	defer func() { gpuDataSource = saved }()
	gpuDataSource = fakeGPUDataSource{attrs: map[string]fullGPUAttrs{
		"H200": {ProductName: "NVIDIA H200", Brand: "Nvidia", Architecture: "Hopper", MemoryMiB: 143771},
	}}

	node := draNode("node-1", map[string]string{draDriverLabelKey: "gpu.nvidia.com"})
	it := &InstanceType{InstanceType: "p5e.48xlarge", GPU: 1, GPUShortName: "H200", GPUMemoryMiB: 144000}

	slices := buildResourceSlicesFromTemplate(node, it)
	require.Len(t, slices, 1)
	cap, ok := slices[0].Spec.Devices[0].Capacity["memory"]
	require.True(t, ok)
	assert.Equal(t, "143771Mi", cap.Value.String(), "capacity should use the override, not EC2's nominal GPUMemoryMiB")
}

func TestBuildResourceSlicesFromTemplate_FullGPU_UnknownSKU(t *testing.T) {
	node := draNode("node-1", map[string]string{draDriverLabelKey: "gpu.nvidia.com"})
	it := &InstanceType{InstanceType: "gNext.xlarge", GPU: 1, GPUShortName: "B200", GPUMemoryMiB: 0}

	slices := buildResourceSlicesFromTemplate(node, it)

	assert.Len(t, slices, 1)
	dev := slices[0].Spec.Devices[0]
	// type is always present; richer attributes absent for an unmapped short name.
	assertStringAttr(t, dev, "type", "gpu")
	_, hasProduct := dev.Attributes["productName"]
	assert.False(t, hasProduct, "unknown SKU should not carry productName")
	assert.Nil(t, dev.Capacity, "no memory capacity when GPUMemoryMiB is unknown")
}

// TestBuildMIGResourceSlices_RTXPro6000 checks the structure against the values verified
// from a real RTX PRO 6000 g7e node: 1 counters slice + 1 devices slice per GPU, each
// device slice holding the whole-GPU device plus the plain MIG (profile x placement) set.
func TestBuildMIGResourceSlices_RTXPro6000(t *testing.T) {
	node := draNode("node-1", map[string]string{
		draDriverLabelKey:     "gpu.nvidia.com",
		draMIGEnabledLabelKey: "true",
	})
	it := &InstanceType{InstanceType: "g7e.12xlarge", GPU: 2, GPUShortName: rtxPro6000ShortName, GPUMemoryMiB: 98304}

	slices := buildResourceSlicesFromTemplate(node, it)

	// 1 counters slice + one devices slice per GPU (2) = 3.
	assert.Len(t, slices, 3)
	counters := slices[0]
	assert.Len(t, counters.Spec.SharedCounters, 2, "one CounterSet per GPU")
	assert.Empty(t, counters.Spec.Devices)
	// Counter set carries the bulk memory counter and per-slice counters (12 slices).
	cs := counters.Spec.SharedCounters[0].Counters
	_, hasBulkMem := cs[ctrMemory]
	assert.True(t, hasBulkMem, "counter set should have a bulk memory counter")
	_, hasHyphenEngine := cs[ctrCopyEngines]
	assert.True(t, hasHyphenEngine, "counter set uses hyphenated engine names")
	assert.Contains(t, cs, memorySliceCounterPrefix+"11", "12 memory slices -> memory-slice-11 present")

	for g, devicesSlice := range slices[1:] {
		assert.Empty(t, devicesSlice.Spec.SharedCounters)
		assert.Equal(t, "node-1", devicesSlice.Spec.Pool.Name)
		// whole-GPU (1) + 1g.24gb (4) + 2g.48gb (2) + 4g.96gb (1) = 8 devices.
		assert.Len(t, devicesSlice.Spec.Devices, 8, "GPU %d device count", g)

		var sawWhole, saw1g bool
		for _, d := range devicesSlice.Spec.Devices {
			if profile, ok := d.Attributes[migProfileAttr]; ok {
				assert.Equal(t, "mig", *d.Attributes["type"].StringValue, "MIG device %s type", d.Name)
				// capacity uses camelCase, counters use hyphens.
				_, hasCamelCap := d.Capacity[capCopyEngines]
				assert.True(t, hasCamelCap, "device %s capacity uses camelCase", d.Name)
				assert.Len(t, d.ConsumesCounters, 1)
				if *profile.StringValue == "1g.24gb" {
					saw1g = true
				}
			} else {
				sawWhole = true
				assert.Equal(t, "gpu", *d.Attributes["type"].StringValue, "whole-GPU device type")
			}
			// Identity attributes present on every device.
			assertStringAttr(t, d, "productName", "NVIDIA RTX PRO 6000 Blackwell Server Edition")
			assertStringAttr(t, d, "architecture", "Blackwell")
		}
		assert.True(t, sawWhole, "GPU %d missing whole-GPU device", g)
		assert.True(t, saw1g, "GPU %d missing 1g.24gb MIG device", g)
	}
}

// TestBuildMIGResourceSlices_A100Structure checks the (UNVERIFIED) A100 table produces a
// well-formed slice set; values are placeholders pending a real capture.
func TestBuildMIGResourceSlices_A100Structure(t *testing.T) {
	node := draNode("node-1", map[string]string{
		draDriverLabelKey:     "gpu.nvidia.com",
		draMIGEnabledLabelKey: "true",
	})
	it := &InstanceType{InstanceType: "p4d.24xlarge", GPU: 1, GPUShortName: "A100", GPUMemoryMiB: 40960}

	slices := buildResourceSlicesFromTemplate(node, it)
	assert.Len(t, slices, 2, "1 counters + 1 devices slice for a single GPU")
	// whole-GPU (1) + 1g.5gb (7) + 2g.10gb (3) + 3g.20gb (2) + 7g.40gb (1) = 14.
	assert.Len(t, slices[1].Spec.Devices, 14)
}

// TestBuildMIGResourceSlices_DeviceChunking checks that a physical GPU whose MIG devices
// exceed resourceapi.ResourceSliceMaxDevices gets split across multiple ResourceSlices instead
// of producing a single oversized (API-invalid) slice, and that Pool.ResourceSliceCount agrees
// with the actual number of slices produced.
func TestBuildMIGResourceSlices_DeviceChunking(t *testing.T) {
	saved := gpuDataSource
	defer func() { gpuDataSource = saved }()

	// 1 whole-GPU device + 150 placements of one profile = 151 devices for the single GPU,
	// comfortably over the 128-device-per-slice API limit.
	placements := make([]int, 150)
	for i := range placements {
		placements[i] = i
	}
	gpuDataSource = fakeGPUDataSource{migs: map[string][]migVariant{
		"DenseGPU": {{
			GPUMemoryMiB: 1000,
			ProductName:  "Dense Test GPU",
			Brand:        "Nvidia",
			Architecture: "Test",
			MemorySlices: 200,
			Whole:        engineCounts{Memory: "1Gi"},
			Profiles: []migProfile{
				{Name: "1g.1gb", ProfileID: 0, MemorySlices: 1, Placements: placements, Engines: engineCounts{Memory: "1Mi"}},
			},
		}},
	}}

	node := draNode("node-1", map[string]string{
		draDriverLabelKey:     "gpu.nvidia.com",
		draMIGEnabledLabelKey: "true",
	})
	it := &InstanceType{InstanceType: "dense.xlarge", GPU: 1, GPUShortName: "DenseGPU", GPUMemoryMiB: 1000}

	slices := buildResourceSlicesFromTemplate(node, it)
	require.NotEmpty(t, slices)

	// 1 counters slice + ceil(151/128) = 2 devices slices for the single GPU = 3.
	require.Len(t, slices, 3)

	totalDevices := 0
	for _, s := range slices[1:] {
		assert.LessOrEqual(t, len(s.Spec.Devices), resourceapi.ResourceSliceMaxDevices, "no slice should exceed the API device limit")
		totalDevices += len(s.Spec.Devices)
	}
	assert.Equal(t, 151, totalDevices, "no devices should be dropped by chunking")

	for _, s := range slices {
		assert.Equal(t, int64(len(slices)), s.Spec.Pool.ResourceSliceCount, "every slice in the pool must agree on the total slice count")
	}
}

func TestBuildMIGResourceSlices_UnknownSKU(t *testing.T) {
	node := draNode("node-1", map[string]string{
		draDriverLabelKey:     "gpu.nvidia.com",
		draMIGEnabledLabelKey: "true",
	})
	// L4 has no MIG table -> no slices, so the node group won't falsely trigger scale-up.
	it := &InstanceType{InstanceType: "g6.xlarge", GPU: 1, GPUShortName: "L4", GPUMemoryMiB: 24576}

	slices := buildResourceSlicesFromTemplate(node, it)
	assert.Nil(t, slices)
}

// TestBuildMIGResourceSlices_MemoryMismatch checks that a GPU whose EC2-reported per-device
// memory doesn't closely match any ConfigMap variant for its short name fails safe (no
// slices) instead of picking the nearest variant regardless of distance — picking, e.g., the
// A100 fixture's 40GiB variant for an 80GiB instance would advertise the wrong profiles.
func TestBuildMIGResourceSlices_MemoryMismatch(t *testing.T) {
	node := draNode("node-1", map[string]string{
		draDriverLabelKey:     "gpu.nvidia.com",
		draMIGEnabledLabelKey: "true",
	})
	// testGPUDataSource's only A100 variant is 40960 MiB; 80000 is far outside tolerance.
	it := &InstanceType{InstanceType: "p4de.24xlarge", GPU: 1, GPUShortName: "A100", GPUMemoryMiB: 80000}

	slices := buildResourceSlicesFromTemplate(node, it)
	assert.Nil(t, slices, "GPU memory too far from any known variant should not advertise MIG slices")
}

func assertStringAttr(t *testing.T, dev resourceapi.Device, key, want string) {
	t.Helper()
	attr, ok := dev.Attributes[resourceapi.QualifiedName(key)]
	if assert.True(t, ok, "missing attribute %q", key) {
		if assert.NotNil(t, attr.StringValue, "attribute %q has no string value", key) {
			assert.Equal(t, want, *attr.StringValue)
		}
	}
}
