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
// The tables are read from a ConfigMap at runtime (see aws_dra_config.go,
// draGPUDataSource), populated from a real node's published ResourceSlice (capture with
// `kubectl get resourceslice -o json`) rather than compiled into the binary — see
// draGPUDataSource.migVariants. Only the plain profiles are modelled — the driver also
// publishes +gfx/+me/+me.all media/graphics variants, which are omitted here (a claim for
// a plain profile does not need them).

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
// counter consumption (a MIG device statically consumes exactly what it exposes). Fields are
// exported with JSON/YAML tags so this same type is both the internal representation used
// throughout this file AND the ConfigMap-unmarshal target (see aws_dra_config.go) — no
// separate mirror struct/converter layer.
type engineCounts struct {
	Multiprocessors int64  `json:"multiprocessors,omitempty"`
	CopyEngines     int64  `json:"copyEngines,omitempty"`
	Decoders        int64  `json:"decoders,omitempty"`
	Encoders        int64  `json:"encoders,omitempty"`
	JPEGEngines     int64  `json:"jpegEngines,omitempty"`
	OFAEngines      int64  `json:"ofaEngines,omitempty"`
	Memory          string `json:"memory,omitempty"` // resource.Quantity string, e.g. "24192Mi" or "95Gi"
}

// migProfile is one MIG profile and where it may be placed on the GPU's memory slices.
type migProfile struct {
	Name         string       `json:"name"`         // bare profile attribute value, e.g. "1g.24gb"
	ProfileID    int          `json:"profileID"`    // NVML profile id, used only in the device name
	MemorySlices int          `json:"memorySlices"` // contiguous memory slices the profile occupies
	Placements   []int        `json:"placements"`   // valid starting memory-slice offsets; one device per placement
	Engines      engineCounts `json:"engines"`
}

// migVariant is one memory variant of a GPU model: its whole-GPU budget, identity
// attributes, and available profiles. Selected by matching EC2 per-device GPU memory.
type migVariant struct {
	GPUMemoryMiB          int64        `json:"gpuMemoryMiB"` // matches EC2 GpuInfo memory; see selectMIGVariant
	ProductName           string       `json:"productName"`
	Brand                 string       `json:"brand"`
	Architecture          string       `json:"architecture"`
	CudaComputeCapability string       `json:"cudaComputeCapability,omitempty"` // NVML version string, e.g. "12.0.0"
	MemorySlices          int          `json:"memorySlices"`                    // total memory slices on the GPU
	Whole                 engineCounts `json:"whole"`
	Profiles              []migProfile `json:"profiles"`
}

// validate checks every resource.Quantity string an operator can supply in the ConfigMap
// before it reaches this file's resource.MustParse calls, which panic on an invalid quantity.
// A variant failing validation is dropped entirely rather than partially applied (see
// aws_dra_config.go, configMapGPUDataSource.migVariants).
func (v migVariant) validate() error {
	if v.GPUMemoryMiB <= 0 {
		return fmt.Errorf("gpuMemoryMiB must be positive, got %d", v.GPUMemoryMiB)
	}
	if _, err := resource.ParseQuantity(v.Whole.Memory); err != nil {
		return fmt.Errorf("whole.memory %q: %w", v.Whole.Memory, err)
	}
	for i, p := range v.Profiles {
		if _, err := resource.ParseQuantity(p.Engines.Memory); err != nil {
			return fmt.Errorf("profiles[%d] (%s) engines.memory %q: %w", i, p.Name, p.Engines.Memory, err)
		}
	}
	return nil
}

// migVariantMatchToleranceFraction bounds how far a variant's gpuMemoryMiB may diverge from
// EC2's reported per-device memory (as a fraction of the variant's memory) and still be
// treated as the same physical SKU — e.g. the RTX PRO Server 6000's EC2-reported 98304MiB
// vs. its driver-verified "whole.memory" of 95Gi/97280MiB is a real, observed ~1% gap. A flat
// MiB tolerance doesn't generalize across GPU memory scales, so this is proportional, with a
// floor for small-memory GPUs where a flat percentage would be too tight.
const (
	migVariantMatchToleranceFraction = 0.02 // 2%, double the largest gap observed so far
	migVariantMatchToleranceFloorMiB = 1024
)

// migVariantMatchTolerance returns the max acceptable |EC2 memory - variant memory| delta for
// a variant with the given gpuMemoryMiB. It deliberately rejects picking, say, a 40GiB A100
// variant for an 80GiB A100 instance: a wrong variant advertises the wrong
// profiles/placements/capacities and can pass a claim CA cannot satisfy.
func migVariantMatchTolerance(variantGPUMemoryMiB int64) int64 {
	tolerance := int64(float64(variantGPUMemoryMiB) * migVariantMatchToleranceFraction)
	if tolerance < migVariantMatchToleranceFloorMiB {
		return migVariantMatchToleranceFloorMiB
	}
	return tolerance
}

// selectMIGVariant picks the variant of a GPU model whose per-device memory matches EC2's
// report within migVariantMatchTolerance. Returns false if the model has no MIG table in the
// ConfigMap-backed data source, or no variant is close enough to trust (see aws_dra_config.go,
// gpuDataSource; today's ConfigMap ships VERIFIED tables for RTX PRO 6000 and H100,
// transcribed from real nodes' published ResourceSlices, and an UNVERIFIED placeholder for
// A100).
func selectMIGVariant(shortName string, gpuMemoryMiB int64) (migVariant, bool) {
	variants, ok := gpuDataSource.migVariants(shortName)
	if !ok || len(variants) == 0 {
		return migVariant{}, false
	}
	memoryDelta := func(v migVariant) int64 {
		delta := v.GPUMemoryMiB - gpuMemoryMiB
		if delta < 0 {
			return -delta
		}
		return delta
	}
	best := variants[0]
	bestDelta := memoryDelta(variants[0])
	for _, v := range variants[1:] {
		if delta := memoryDelta(v); delta < bestDelta {
			best, bestDelta = v, delta
		}
	}
	if bestDelta > migVariantMatchTolerance(best.GPUMemoryMiB) {
		return migVariant{}, false
	}
	return best, true
}

// buildMIGResourceSlices builds the counters slice plus one devices slice per GPU for a
// MIG-enabled node group, matching the real driver's shape. Returns nil if the SKU has no
// MIG table (so the node group fails safe rather than advertising a bogus inventory).
//
// A single physical GPU's devices are split across multiple ResourceSlices if they exceed
// resourceapi.ResourceSliceMaxDevices (128) — the API rejects a slice over that limit. No known
// NVIDIA MIG GPU today has anywhere near 128 (profile x placement) devices on one physical GPU
// (H100, the densest table here, has 18), so this is defensive rather than reachable with
// today's hardware; it's here so a future denser MIG geometry fails a k8s API validation error
// at slice-creation time on the real node rather than silently producing an invalid template.
func buildMIGResourceSlices(node *apiv1.Node, instanceType *InstanceType, driver string) []*resourceapi.ResourceSlice {
	v, ok := selectMIGVariant(instanceType.GPUShortName, instanceType.GPUMemoryMiB)
	if !ok {
		klog.Warningf("DRA MIG enabled for node group with GPU %q but no MIG profile table exists; not advertising MIG ResourceSlices for %s", instanceType.GPUShortName, node.Name)
		return nil
	}

	nodeName := node.Name
	gpuCount := int(instanceType.GPU)

	perGPUDevices := make([][]resourceapi.Device, gpuCount)
	for g := 0; g < gpuCount; g++ {
		devices := []resourceapi.Device{wholeGPUDevice(g, v)}
		for _, p := range v.Profiles {
			for _, start := range p.Placements {
				devices = append(devices, migDevice(g, v, p, start))
			}
		}
		perGPUDevices[g] = devices
	}

	totalSlices := int64(1) // counters slice
	for _, devices := range perGPUDevices {
		totalSlices += int64(deviceChunkCount(len(devices)))
	}
	slices := make([]*resourceapi.ResourceSlice, 0, totalSlices)

	counters := &resourceapi.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%s-counters", nodeName, driver)},
		Spec:       newSliceSpec(driver, nodeName, totalSlices),
	}
	for g := 0; g < gpuCount; g++ {
		counters.Spec.SharedCounters = append(counters.Spec.SharedCounters, counterSetForGPU(counterSetName(g), v))
	}
	slices = append(slices, counters)

	for g, devices := range perGPUDevices {
		for chunkIndex, chunk := range chunkDevices(devices) {
			spec := newSliceSpec(driver, nodeName, totalSlices)
			spec.Devices = chunk
			slices = append(slices, &resourceapi.ResourceSlice{
				ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%s-devices-%d-%d", nodeName, driver, g, chunkIndex)},
				Spec:       spec,
			})
		}
	}
	return slices
}

// deviceChunkCount returns how many resourceapi.ResourceSliceMaxDevices-sized ResourceSlices n
// devices need.
func deviceChunkCount(n int) int {
	return (n + resourceapi.ResourceSliceMaxDevices - 1) / resourceapi.ResourceSliceMaxDevices
}

// chunkDevices splits devices into resourceapi.ResourceSliceMaxDevices-sized slices.
func chunkDevices(devices []resourceapi.Device) [][]resourceapi.Device {
	var chunks [][]resourceapi.Device
	for len(devices) > 0 {
		n := min(len(devices), resourceapi.ResourceSliceMaxDevices)
		chunks = append(chunks, devices[:n])
		devices = devices[n:]
	}
	return chunks
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
	counters := engineCountersHyphen(v.Whole)
	for s := 0; s < v.MemorySlices; s++ {
		counters[fmt.Sprintf("%s%d", memorySliceCounterPrefix, s)] = counterQty(1)
	}
	return resourceapi.CounterSet{Name: name, Counters: counters}
}

// wholeGPUDevice is the full-GPU device the driver publishes even in MIG mode; it consumes
// every counter, so allocating it excludes all MIG partitions and vice versa.
func wholeGPUDevice(gpuIndex int, v migVariant) resourceapi.Device {
	consumed := engineCountersHyphen(v.Whole)
	for s := 0; s < v.MemorySlices; s++ {
		consumed[fmt.Sprintf("%s%d", memorySliceCounterPrefix, s)] = counterQty(1)
	}
	return resourceapi.Device{
		Name:       fmt.Sprintf("gpu-%d", gpuIndex),
		Attributes: gpuIdentityAttributes(v, gpuDeviceType, ""),
		Capacity:   engineCapacity(v.Whole),
		ConsumesCounters: []resourceapi.DeviceCounterConsumption{
			{CounterSet: counterSetName(gpuIndex), Counters: consumed},
		},
	}
}

// migDevice is one (profile x placement) partition on a GPU. It consumes the contiguous
// memory-slice span [start, start+memorySlices) plus its engine share, which is how the
// scheduler enforces non-overlap.
func migDevice(gpuIndex int, v migVariant, p migProfile, start int) resourceapi.Device {
	consumed := engineCountersHyphen(p.Engines)
	for s := start; s < start+p.MemorySlices; s++ {
		consumed[fmt.Sprintf("%s%d", memorySliceCounterPrefix, s)] = counterQty(1)
	}
	return resourceapi.Device{
		Name:       fmt.Sprintf("gpu-%d-mig-%s-%d-%d", gpuIndex, sanitizeProfileName(p.Name), p.ProfileID, start),
		Attributes: gpuIdentityAttributes(v, migDeviceType, p.Name),
		Capacity:   engineCapacity(p.Engines),
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
		"type": {StringValue: ptr.To(deviceType)},
	}
	if v.ProductName != "" {
		attrs["productName"] = resourceapi.DeviceAttribute{StringValue: ptr.To(v.ProductName)}
	}
	if v.Brand != "" {
		attrs["brand"] = resourceapi.DeviceAttribute{StringValue: ptr.To(v.Brand)}
	}
	if v.Architecture != "" {
		attrs["architecture"] = resourceapi.DeviceAttribute{StringValue: ptr.To(v.Architecture)}
	}
	if v.CudaComputeCapability != "" {
		attrs["cudaComputeCapability"] = resourceapi.DeviceAttribute{VersionValue: ptr.To(v.CudaComputeCapability)}
	}
	if profile != "" {
		attrs[migProfileAttr] = resourceapi.DeviceAttribute{StringValue: ptr.To(profile)}
	}
	return attrs
}

// engineCountersHyphen builds the hyphenated counter map (CounterSet / ConsumesCounters).
func engineCountersHyphen(e engineCounts) map[string]resourceapi.Counter {
	return map[string]resourceapi.Counter{
		ctrMultiprocessors: counterQty(e.Multiprocessors),
		ctrCopyEngines:     counterQty(e.CopyEngines),
		ctrDecoders:        counterQty(e.Decoders),
		ctrEncoders:        counterQty(e.Encoders),
		ctrJPEGEngines:     counterQty(e.JPEGEngines),
		ctrOFAEngines:      counterQty(e.OFAEngines),
		ctrMemory:          {Value: resource.MustParse(e.Memory)},
	}
}

// engineCapacity builds the camelCase capacity map on a device.
func engineCapacity(e engineCounts) map[resourceapi.QualifiedName]resourceapi.DeviceCapacity {
	return map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{
		capMultiprocessors: {Value: *resource.NewQuantity(e.Multiprocessors, resource.DecimalSI)},
		capCopyEngines:     {Value: *resource.NewQuantity(e.CopyEngines, resource.DecimalSI)},
		capDecoders:        {Value: *resource.NewQuantity(e.Decoders, resource.DecimalSI)},
		capEncoders:        {Value: *resource.NewQuantity(e.Encoders, resource.DecimalSI)},
		capJPEGEngines:     {Value: *resource.NewQuantity(e.JPEGEngines, resource.DecimalSI)},
		capOFAEngines:      {Value: *resource.NewQuantity(e.OFAEngines, resource.DecimalSI)},
		capMemory:          {Value: resource.MustParse(e.Memory)},
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
