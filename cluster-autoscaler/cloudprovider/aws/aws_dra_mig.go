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

	apiv1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	klog "k8s.io/klog/v2"
	"k8s.io/utils/ptr"
)

// MIG (Multi-Instance GPU) scale-from-zero support, the Phase 2 extension of aws_dra.go.
//
// A MIG-capable GPU can be carved into isolated partitions ("profiles"); with dynamic MIG
// + DRA the NVIDIA driver reconfigures partitions per-pod at schedule time, so one
// MIG-enabled node group replaces the combinatorial explosion of per-profile node groups.
// For scale-from-zero the scheduler must be able to allocate a MIG ResourceClaim against
// the template node, so this file reproduces the partitionable-device ResourceSlices the
// driver publishes at runtime (KEP-4815).
//
// SHAPE (verified against a real RTX PRO 6000 Blackwell g7e node):
//
//   - one counters slice: SharedCounters, one CounterSet per physical GPU, no devices
//   - one devices slice PER physical GPU: the whole-GPU device plus every MIG
//     (profile x placement), each with ConsumesCounters referencing its GPU's CounterSet
//
// So a node with G GPUs yields 1 + G ResourceSlices, all sharing (Driver, Pool.Name).
//
// Two counter-name conventions, both verbatim from the driver: CounterSet / ConsumesCounters
// use hyphenated names (copy-engines), while device Capacity uses camelCase (copyEngines).
//
// The tables are hand-written from a real node's published ResourceSlice (capture with
// `kubectl get resourceslice -o json`). RTX PRO 6000 is transcribed from a verified
// capture; other SKUs are UNVERIFIED guesses until captured. Only the plain profiles are
// modelled — the driver also publishes +gfx/+me/+me.all media/graphics variants, which are
// omitted here (a claim for a plain profile does not need them).

const (
	// migProfileAttr is the device attribute carrying the MIG profile name (e.g. "1g.24gb").
	// It is a bare (driver-domain) attribute name, matching what the driver emits and what
	// claim CEL selects on: device.attributes['gpu.nvidia.com'].profile.
	migProfileAttr = "profile"
	// migDeviceType is the value of the "type" attribute on a MIG device (whole-GPU devices
	// use gpuDeviceType = "gpu").
	migDeviceType = "mig"

	// Counter names for CounterSet / ConsumesCounters (hyphenated, verbatim from the driver).
	ctrCopyEngines           = "copy-engines"
	ctrDecoders              = "decoders"
	ctrEncoders              = "encoders"
	ctrJPEGEngines           = "jpeg-engines"
	ctrOFAEngines            = "ofa-engines"
	ctrMemory                = "memory"
	ctrMultiprocessors       = "multiprocessors"
	memorySliceCounterPrefix = "memory-slice-"

	// Capacity names on devices (camelCase, verbatim from the driver).
	capCopyEngines     = "copyEngines"
	capDecoders        = "decoders"
	capEncoders        = "encoders"
	capJPEGEngines     = "jpegEngines"
	capOFAEngines      = "ofaEngines"
	capMemory          = "memory"
	capMultiprocessors = "multiprocessors"

	// rtxPro6000ShortName is the EC2 GpuInfo short name for the NVIDIA RTX PRO Server 6000
	// Blackwell (g7e instance family), as returned by DescribeInstanceTypes.
	rtxPro6000ShortName = "RTX PRO Server 6000"
)

// engineCounts is the per-device hardware-unit budget shared by a device's capacity and its
// counter consumption (a MIG device statically consumes exactly what it exposes).
type engineCounts struct {
	multiprocessors int64
	copyEngines     int64
	decoders        int64
	encoders        int64
	jpegEngines     int64
	ofaEngines      int64
	memory          string // resource.Quantity string, e.g. "24192Mi" or "95Gi"
}

// migProfile is one MIG profile and where it may be placed on the GPU's memory slices.
type migProfile struct {
	name         string // bare profile attribute value, e.g. "1g.24gb"
	profileID    int    // NVML profile id, used only in the device name
	memorySlices int    // contiguous memory slices the profile occupies
	placements   []int  // valid starting memory-slice offsets; one device per placement
	engines      engineCounts
}

// migVariant is one memory variant of a GPU model: its whole-GPU budget, identity
// attributes, and available profiles. Selected by matching EC2 per-device GPU memory.
type migVariant struct {
	gpuMemoryMiB          int64 // matches EC2 GpuInfo memory; closest-match selection
	productName           string
	brand                 string
	architecture          string
	cudaComputeCapability string // NVML version string, e.g. "12.0.0"
	memorySlices          int    // total memory slices on the GPU
	whole                 engineCounts
	profiles              []migProfile
}

// migProfileTables maps EC2 GPU short name -> memory variants of that model.
//
// RTX PRO 6000 is VERIFIED (transcribed from a real g7e node). A100 is UNVERIFIED and
// must be replaced by a real capture before use.
var migProfileTables = map[string][]migVariant{
	// VERIFIED: NVIDIA RTX PRO 6000 Blackwell Server Edition (AWS g7e). 12 memory slices,
	// 188 SMs, 96GB. Plain profiles only (media/graphics variants omitted).
	rtxPro6000ShortName: {{
		gpuMemoryMiB:          98304, // ~96GiB; sole variant, so value only affects logging
		productName:           "NVIDIA RTX PRO 6000 Blackwell Server Edition",
		brand:                 "Nvidia",
		architecture:          "Blackwell",
		cudaComputeCapability: "12.0.0",
		memorySlices:          12,
		whole:                 engineCounts{multiprocessors: 188, copyEngines: 4, decoders: 4, encoders: 4, jpegEngines: 4, ofaEngines: 1, memory: "95Gi"},
		profiles: []migProfile{
			{name: "1g.24gb", profileID: 14, memorySlices: 3, placements: []int{0, 3, 6, 9}, engines: engineCounts{multiprocessors: 46, copyEngines: 1, decoders: 1, encoders: 1, jpegEngines: 1, ofaEngines: 0, memory: "24192Mi"}},
			{name: "2g.48gb", profileID: 5, memorySlices: 6, placements: []int{0, 6}, engines: engineCounts{multiprocessors: 94, copyEngines: 2, decoders: 2, encoders: 2, jpegEngines: 2, ofaEngines: 0, memory: "48512Mi"}},
			{name: "4g.96gb", profileID: 0, memorySlices: 12, placements: []int{0}, engines: engineCounts{multiprocessors: 188, copyEngines: 4, decoders: 4, encoders: 4, jpegEngines: 4, ofaEngines: 1, memory: "95Gi"}},
		},
	}},

	// UNVERIFIED: NVIDIA A100 (AWS p4d/p4de). Placeholder structure; every value must be
	// replaced by a real capture (kubectl get resourceslice) before production use.
	"A100": {{
		gpuMemoryMiB:          40960,
		productName:           "NVIDIA A100-SXM4-40GB",
		brand:                 "Nvidia",
		architecture:          "Ampere",
		cudaComputeCapability: "8.0.0",
		memorySlices:          8,
		whole:                 engineCounts{multiprocessors: 108, copyEngines: 7, decoders: 5, encoders: 0, jpegEngines: 1, ofaEngines: 1, memory: "40Gi"},
		profiles: []migProfile{
			{name: "1g.5gb", profileID: 19, memorySlices: 1, placements: []int{0, 1, 2, 3, 4, 5, 6}, engines: engineCounts{multiprocessors: 14, copyEngines: 1, memory: "5Gi"}},
			{name: "2g.10gb", profileID: 14, memorySlices: 2, placements: []int{0, 2, 4}, engines: engineCounts{multiprocessors: 28, copyEngines: 2, decoders: 1, memory: "10Gi"}},
			{name: "3g.20gb", profileID: 9, memorySlices: 4, placements: []int{0, 4}, engines: engineCounts{multiprocessors: 42, copyEngines: 3, decoders: 2, memory: "20Gi"}},
			{name: "7g.40gb", profileID: 0, memorySlices: 8, placements: []int{0}, engines: engineCounts{multiprocessors: 98, copyEngines: 7, decoders: 5, jpegEngines: 1, ofaEngines: 1, memory: "40Gi"}},
		},
	}},
}

// selectMIGVariant picks the variant of a GPU model whose per-device memory best matches
// EC2's report. Returns false if the model has no MIG table.
func selectMIGVariant(shortName string, gpuMemoryMiB int64) (migVariant, bool) {
	variants, ok := migProfileTables[shortName]
	if !ok || len(variants) == 0 {
		return migVariant{}, false
	}
	best := variants[0]
	bestDelta := absInt64(variants[0].gpuMemoryMiB - gpuMemoryMiB)
	for _, v := range variants[1:] {
		if delta := absInt64(v.gpuMemoryMiB - gpuMemoryMiB); delta < bestDelta {
			best, bestDelta = v, delta
		}
	}
	return best, true
}

func absInt64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// buildMIGResourceSlices builds the counters slice plus one devices slice per GPU for a
// MIG-enabled node group, matching the real driver's shape. Returns nil if the SKU has no
// MIG table (so the node group fails safe rather than advertising a bogus inventory).
func buildMIGResourceSlices(node *apiv1.Node, instanceType *InstanceType, driver string) []*resourceapi.ResourceSlice {
	v, ok := selectMIGVariant(instanceType.GPUShortName, instanceType.GPUMemoryMiB)
	if !ok {
		klog.Warningf("DRA MIG enabled for node group with GPU %q but no MIG profile table exists; not advertising MIG ResourceSlices for %s", instanceType.GPUShortName, node.Name)
		return nil
	}

	nodeName := node.Name
	gpuCount := int(instanceType.GPU)
	totalSlices := int64(1 + gpuCount) // 1 counters slice + 1 devices slice per GPU
	slices := make([]*resourceapi.ResourceSlice, 0, gpuCount+1)

	counters := &resourceapi.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%s-counters", nodeName, driver)},
		Spec:       newSliceSpec(driver, nodeName, totalSlices),
	}
	for g := 0; g < gpuCount; g++ {
		counters.Spec.SharedCounters = append(counters.Spec.SharedCounters, counterSetForGPU(counterSetName(g), v))
	}
	slices = append(slices, counters)

	for g := 0; g < gpuCount; g++ {
		devices := &resourceapi.ResourceSlice{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%s-devices-%d", nodeName, driver, g)},
			Spec:       newSliceSpec(driver, nodeName, totalSlices),
		}
		devices.Spec.Devices = append(devices.Spec.Devices, wholeGPUDevice(g, v))
		for _, p := range v.profiles {
			for _, start := range p.placements {
				devices.Spec.Devices = append(devices.Spec.Devices, migDevice(g, v, p, start))
			}
		}
		slices = append(slices, devices)
	}
	return slices
}

func newSliceSpec(driver, nodeName string, totalSliceCount int64) resourceapi.ResourceSliceSpec {
	nn := nodeName
	return resourceapi.ResourceSliceSpec{
		Driver:   driver,
		NodeName: &nn,
		Pool:     resourceapi.ResourcePool{Name: nodeName, ResourceSliceCount: totalSliceCount},
	}
}

func counterSetName(gpuIndex int) string {
	return fmt.Sprintf("gpu-%d-counter-set", gpuIndex)
}

// counterSetForGPU builds one physical GPU's divisible budget: one counter per memory slice
// plus the bulk memory and hardware-unit totals.
func counterSetForGPU(name string, v migVariant) resourceapi.CounterSet {
	counters := engineCountersHyphen(v.whole)
	for s := 0; s < v.memorySlices; s++ {
		counters[fmt.Sprintf("%s%d", memorySliceCounterPrefix, s)] = counterQty(1)
	}
	return resourceapi.CounterSet{Name: name, Counters: counters}
}

// wholeGPUDevice is the full-GPU device the driver publishes even in MIG mode; it consumes
// every counter, so allocating it excludes all MIG partitions and vice versa.
func wholeGPUDevice(gpuIndex int, v migVariant) resourceapi.Device {
	consumed := engineCountersHyphen(v.whole)
	for s := 0; s < v.memorySlices; s++ {
		consumed[fmt.Sprintf("%s%d", memorySliceCounterPrefix, s)] = counterQty(1)
	}
	return resourceapi.Device{
		Name:       fmt.Sprintf("gpu-%d", gpuIndex),
		Attributes: gpuIdentityAttributes(v, gpuDeviceType, ""),
		Capacity:   engineCapacity(v.whole),
		ConsumesCounters: []resourceapi.DeviceCounterConsumption{
			{CounterSet: counterSetName(gpuIndex), Counters: consumed},
		},
	}
}

// migDevice is one (profile x placement) partition on a GPU. It consumes the contiguous
// memory-slice span [start, start+memorySlices) plus its engine share, which is how the
// scheduler enforces non-overlap.
func migDevice(gpuIndex int, v migVariant, p migProfile, start int) resourceapi.Device {
	consumed := engineCountersHyphen(p.engines)
	for s := start; s < start+p.memorySlices; s++ {
		consumed[fmt.Sprintf("%s%d", memorySliceCounterPrefix, s)] = counterQty(1)
	}
	return resourceapi.Device{
		Name:       fmt.Sprintf("gpu-%d-mig-%s-%d-%d", gpuIndex, sanitizeProfileName(p.name), p.profileID, start),
		Attributes: gpuIdentityAttributes(v, migDeviceType, p.name),
		Capacity:   engineCapacity(p.engines),
		ConsumesCounters: []resourceapi.DeviceCounterConsumption{
			{CounterSet: counterSetName(gpuIndex), Counters: consumed},
		},
	}
}

// gpuIdentityAttributes returns the identity attributes common to whole-GPU and MIG devices.
// profile is added only when non-empty (MIG devices). Runtime-only attributes (uuid,
// driverVersion, cudaDriverVersion, pciBusID, pcieRoot, addressingMode, parentUUID) are
// omitted — they cannot be known pre-scale.
func gpuIdentityAttributes(v migVariant, deviceType, profile string) map[resourceapi.QualifiedName]resourceapi.DeviceAttribute {
	attrs := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
		"type":         {StringValue: ptr.To(deviceType)},
		"productName":  {StringValue: ptr.To(v.productName)},
		"brand":        {StringValue: ptr.To(v.brand)},
		"architecture": {StringValue: ptr.To(v.architecture)},
	}
	if v.cudaComputeCapability != "" {
		attrs["cudaComputeCapability"] = resourceapi.DeviceAttribute{VersionValue: ptr.To(v.cudaComputeCapability)}
	}
	if profile != "" {
		attrs[migProfileAttr] = resourceapi.DeviceAttribute{StringValue: ptr.To(profile)}
	}
	return attrs
}

// engineCountersHyphen builds the hyphenated counter map (CounterSet / ConsumesCounters).
func engineCountersHyphen(e engineCounts) map[string]resourceapi.Counter {
	return map[string]resourceapi.Counter{
		ctrMultiprocessors: counterQty(e.multiprocessors),
		ctrCopyEngines:     counterQty(e.copyEngines),
		ctrDecoders:        counterQty(e.decoders),
		ctrEncoders:        counterQty(e.encoders),
		ctrJPEGEngines:     counterQty(e.jpegEngines),
		ctrOFAEngines:      counterQty(e.ofaEngines),
		ctrMemory:          {Value: resource.MustParse(e.memory)},
	}
}

// engineCapacity builds the camelCase capacity map on a device.
func engineCapacity(e engineCounts) map[resourceapi.QualifiedName]resourceapi.DeviceCapacity {
	return map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{
		capMultiprocessors: {Value: *resource.NewQuantity(e.multiprocessors, resource.DecimalSI)},
		capCopyEngines:     {Value: *resource.NewQuantity(e.copyEngines, resource.DecimalSI)},
		capDecoders:        {Value: *resource.NewQuantity(e.decoders, resource.DecimalSI)},
		capEncoders:        {Value: *resource.NewQuantity(e.encoders, resource.DecimalSI)},
		capJPEGEngines:     {Value: *resource.NewQuantity(e.jpegEngines, resource.DecimalSI)},
		capOFAEngines:      {Value: *resource.NewQuantity(e.ofaEngines, resource.DecimalSI)},
		capMemory:          {Value: resource.MustParse(e.memory)},
	}
}

func counterQty(n int64) resourceapi.Counter {
	return resourceapi.Counter{Value: *resource.NewQuantity(n, resource.DecimalSI)}
}

// sanitizeProfileName turns a profile name like "1g.24gb" into a DNS-label-safe fragment
// ("1g24gb") for use in the device name.
func sanitizeProfileName(name string) string {
	out := make([]rune, 0, len(name))
	for _, r := range name {
		if r == '.' {
			continue
		}
		out = append(out, r)
	}
	return string(out)
}
