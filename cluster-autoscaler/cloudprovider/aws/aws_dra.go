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

// Dynamic Resource Allocation (DRA) scale-from-zero support for GPU node groups.
//
// Cluster Autoscaler cannot make correct scale-from-zero decisions for pods that
// use DRA ResourceClaims (e.g. GPU requests via DRA), because TemplateNodeInfo()
// builds a template node with no ResourceSlices attached. During scale-up the DRA
// scheduler plugin then finds no device inventory to allocate the claim against and
// concludes the node group cannot help the pod, so it never scales up.
//
// This file fabricates the ResourceSlices that the NVIDIA DRA driver would publish
// once the node boots, derived from the EC2 instance type's GPU metadata plus GPU
// attribute data read from a ConfigMap (see aws_dra_config.go, draGPUDataSource). The
// slices are attached to the template node so the scheduler's simulation can allocate
// the claim and trigger scale-up.
//
// Nothing here is intended for upstream: this is a Datadog-specific way of keeping GPU
// attribute data operator-editable without a CA rebuild/redeploy.

const (
	// draDriverLabelKey opts a node group into DRA ResourceSlice generation and names
	// the DRA driver the fabricated slices belong to (e.g. "gpu.nvidia.com"). It is set
	// as a node label on the node group's kubelet labels and reaches the template node
	// via the node-template/label ASG tag path (extractLabelsFromAsg).
	draDriverLabelKey = "node.datadoghq.com/dra-driver"

	// draMIGEnabledLabelKey marks a node group as MIG-enabled. When set to "true", the
	// builder emits MIG profile-placement devices plus counter sets instead of a single
	// full-GPU device. See aws_dra_mig.go.
	draMIGEnabledLabelKey = "node.datadoghq.com/dra-mig-enabled"

	// gpuDeviceType is the value of the "type" device attribute, matching what the
	// NVIDIA DRA driver emits at runtime.
	gpuDeviceType = "gpu"
)

// GPU attribute data, keyed on the EC2 GpuInfo short name (InstanceType.GPUShortName), is
// read from a ConfigMap at runtime via draGPUDataSource (see aws_dra_config.go) rather than
// compiled into the binary. EC2 only exposes the short name (e.g. "A10G", "H100") and
// per-device memory; every other attribute the NVIDIA driver publishes via NVML at runtime
// must be reproduced in the ConfigMap so CEL selectors on those attributes evaluate
// correctly during scale-up simulation. An instance type whose short name is absent from
// the ConfigMap still gets a ResourceSlice with type=gpu and memory capacity, just without
// the richer attributes.

// buildResourceSlicesFromTemplate fabricates the DRA ResourceSlices for a template node,
// reproducing what the NVIDIA DRA driver would publish on the real node. It returns nil
// for node groups that are not DRA-enabled or have no GPUs, leaving non-DRA behaviour
// unchanged.
func buildResourceSlicesFromTemplate(node *apiv1.Node, instanceType *InstanceType) []*resourceapi.ResourceSlice {
	if node == nil || instanceType == nil {
		return nil
	}
	driver := node.Labels[draDriverLabelKey]
	if driver == "" || instanceType.GPU == 0 {
		return nil
	}

	// Log the observed GPU short name so the correct ConfigMap key can be discovered from
	// CA logs (EC2 short names are not always known ahead of time).
	klog.V(4).Infof("DRA: building ResourceSlices for node group GPU %q (%s, driver %s)", instanceType.GPUShortName, instanceType.InstanceType, driver)

	// GPUShortName/GPUMemoryMiB are populated only via the dynamic EC2 API path
	// (aws_util.go), never by the static instance-type list (--aws-use-static-instance-list),
	// so this combination means the operator is running that mode.
	//
	// Two options here: warn and keep going with a degraded full-GPU slice (current behavior),
	// or fail safe and return nil like the "unknown SKU"/no-MIG-table paths do. We warn rather
	// than suppress because the degraded slice is still useful for the common case: a claim
	// selecting only on type=gpu (no attribute/memory constraint) is satisfied correctly by it,
	// and static-instance-list is chosen specifically for private/airgapped clusters where the
	// EC2 API isn't reachable at all — silently returning nil there would make DRA scale-from-
	// zero permanently non-functional for every GPU node group with no operator-visible cause
	// beyond this log line. A claim that does constrain on attributes or memory won't match
	// during simulation either way (missing vs. wrong), so nothing is lost by not suppressing.
	if instanceType.GPUShortName == "" && instanceType.GPUMemoryMiB == 0 {
		klog.Warningf("DRA enabled for node group with GPU instance type %s but GPUShortName/GPUMemoryMiB are unset (likely --aws-use-static-instance-list); fabricated ResourceSlices will be missing attributes and MIG groups will get none", instanceType.InstanceType)
	}

	// MIG-enabled node groups advertise partitionable devices (see aws_dra_mig.go).
	if node.Labels[draMIGEnabledLabelKey] == "true" {
		return buildMIGResourceSlices(node, instanceType, driver)
	}

	return buildFullGPUResourceSlices(node, instanceType, driver)
}

// buildFullGPUResourceSlices builds one ResourceSlice per physical GPU (the Phase 1,
// non-MIG path). The driver emits one slice per GPU, each carrying one device.
func buildFullGPUResourceSlices(node *apiv1.Node, instanceType *InstanceType, driver string) []*resourceapi.ResourceSlice {
	nodeName := node.Name
	fullAttrs, ok := gpuDataSource.fullGPUAttributes(instanceType.GPUShortName)
	attrs := gpuDeviceAttributes(fullAttrs, ok)

	// MemoryMiB, when set, is the driver-verified capacity for this GPU model; EC2's nominal
	// GPUMemoryMiB can overstate what the driver actually publishes (see fullGPUAttrs.MemoryMiB).
	memoryMiB := instanceType.GPUMemoryMiB
	if ok && fullAttrs.MemoryMiB > 0 {
		memoryMiB = fullAttrs.MemoryMiB
	}
	var capacity map[resourceapi.QualifiedName]resourceapi.DeviceCapacity
	if memoryMiB > 0 {
		capacity = map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{
			"memory": {Value: resource.MustParse(fmt.Sprintf("%dMi", memoryMiB))},
		}
	}

	totalSlices := instanceType.GPU // one slice per GPU
	slices := make([]*resourceapi.ResourceSlice, 0, int(instanceType.GPU))
	for i := int64(0); i < instanceType.GPU; i++ {
		slices = append(slices, &resourceapi.ResourceSlice{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%s-%d", nodeName, driver, i)},
			Spec: resourceapi.ResourceSliceSpec{
				Driver:   driver,
				NodeName: &nodeName,
				Pool:     resourceapi.ResourcePool{Name: nodeName, ResourceSliceCount: totalSlices},
				Devices: []resourceapi.Device{{
					Name:       fmt.Sprintf("gpu-%d", i),
					Attributes: attrs,
					Capacity:   capacity,
				}},
			},
		})
	}
	return slices
}

// gpuDeviceAttributes returns the device attributes for a GPU short name's looked-up
// fullGPUAttrs. "type" is always present; the richer attributes are added only when found is
// true (the short name is known to the ConfigMap-backed data source, see aws_dra_config.go,
// gpuDataSource). Runtime-only attributes the driver publishes from NVML/sysfs
// (driverVersion, cudaDriverVersion, uuid, pciBusID, pcieRoot, addressingMode) are
// deliberately omitted — they cannot be known pre-scale, so CEL selectors referencing them
// will not match template slices.
func gpuDeviceAttributes(a fullGPUAttrs, found bool) map[resourceapi.QualifiedName]resourceapi.DeviceAttribute {
	attrs := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
		"type": {StringValue: ptr.To(gpuDeviceType)},
	}
	if !found {
		return attrs
	}
	if a.ProductName != "" {
		attrs["productName"] = resourceapi.DeviceAttribute{StringValue: ptr.To(a.ProductName)}
	}
	if a.Brand != "" {
		attrs["brand"] = resourceapi.DeviceAttribute{StringValue: ptr.To(a.Brand)}
	}
	if a.Architecture != "" {
		attrs["architecture"] = resourceapi.DeviceAttribute{StringValue: ptr.To(a.Architecture)}
	}
	if a.CudaComputeCapability != "" {
		attrs["cudaComputeCapability"] = resourceapi.DeviceAttribute{VersionValue: ptr.To(a.CudaComputeCapability)}
	}
	return attrs
}
