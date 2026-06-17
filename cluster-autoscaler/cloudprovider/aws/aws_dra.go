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
// once the node boots, derived from the EC2 instance type's GPU metadata plus static
// attribute maps. The slices are attached to the template node so the scheduler's
// simulation can allocate the claim and trigger scale-up.
//
// Nothing here is intended for upstream: the static NVIDIA attribute maps rots on
// each new GPU family release and would need a config-driven approach to upstream.

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

// Static GPU attribute maps, keyed on the EC2 GpuInfo short name (InstanceType.GPUShortName).
//
// EC2 only exposes the short name (e.g. "A10G", "H100") and per-device memory. Every
// other attribute the NVIDIA driver publishes via NVML at runtime must be reproduced
// here so that CEL selectors on those attributes evaluate correctly during scale-up
// simulation. These maps must be updated when NVIDIA releases new GPU families. An
// instance type whose short name is absent still gets a ResourceSlice with type=gpu and
// memory capacity, just without the richer attributes.

// gpuFullProductName maps the EC2 short name to the full NVML product name (NVML GetName,
// verbatim). For H100/H200-family this includes the memory/HBM variant suffix.
var gpuFullProductName = map[string]string{
	"T4":   "Tesla T4",
	"V100": "Tesla V100-SXM2-16GB",
	"A100": "NVIDIA A100-SXM4-40GB", // p4d; p4de uses A100-SXM4-80GB
	"A10G": "NVIDIA A10G",
	"L4":   "NVIDIA L4",
	"L40S": "NVIDIA L40S",
	"H100":             "NVIDIA H100 80GB HBM3",
	"H200":             "NVIDIA H200",
	"RTX PRO Server 6000": "NVIDIA RTX PRO 6000 Blackwell Server Edition",
}

// gpuBrand maps the EC2 short name to the NVML brand (NVML GetBrand).
// The driver reports "Nvidia" (not "NVIDIA") regardless of GPU family.
var gpuBrand = map[string]string{
	"T4":   "Nvidia",
	"V100": "Nvidia",
	"A100": "Nvidia",
	"A10G": "Nvidia",
	"L4":   "Nvidia",
	"L40S": "Nvidia",
	"H100":             "Nvidia",
	"H200":             "Nvidia",
	"RTX PRO Server 6000": "Nvidia",
}

// gpuArchitecture maps the EC2 short name to the NVML architecture string
// (NVML GetArchitectureAsString).
var gpuArchitecture = map[string]string{
	"T4":   "Turing",
	"V100": "Volta",
	"A100": "Ampere",
	"A10G": "Ampere",
	"L4":   "Ada Lovelace",
	"L40S": "Ada Lovelace",
	"H100":             "Hopper",
	"H200":             "Hopper",
	"RTX PRO Server 6000": "Blackwell",
}

// gpuCudaComputeCapability maps the EC2 short name to the CUDA compute capability in
// NVML version format (major.minor.0), so CEL selectors like
// device.attributes['cudaComputeCapability'].version.major == 8 evaluate correctly.
var gpuCudaComputeCapability = map[string]string{
	"T4":   "7.5.0",
	"V100": "7.0.0",
	"A100": "8.0.0",
	"A10G": "8.6.0",
	"L4":   "8.9.0",
	"L40S": "8.9.0",
	"H100":             "9.0.0",
	"H200":             "9.0.0",
	"RTX PRO Server 6000": "12.0.0",
}

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

	// Log the observed GPU short name so the correct static-table key can be discovered
	// from CA logs (EC2 short names are not always known ahead of time).
	klog.V(4).Infof("DRA: building ResourceSlices for node group GPU %q (%s, driver %s)", instanceType.GPUShortName, instanceType.InstanceType, driver)

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
	attrs := gpuDeviceAttributes(instanceType.GPUShortName)

	var capacity map[resourceapi.QualifiedName]resourceapi.DeviceCapacity
	if instanceType.GPUMemoryMiB > 0 {
		capacity = map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{
			"memory": {Value: resource.MustParse(fmt.Sprintf("%dMi", instanceType.GPUMemoryMiB))},
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

// gpuDeviceAttributes returns the device attributes for a GPU short name. "type" is always
// present; the richer attributes are added only when the short name is known. Runtime-only
// attributes the driver publishes from NVML/sysfs (driverVersion, cudaDriverVersion, uuid,
// pciBusID, pcieRoot, addressingMode) are deliberately omitted — they cannot be known
// pre-scale, so CEL selectors referencing them will not match template slices.
func gpuDeviceAttributes(shortName string) map[resourceapi.QualifiedName]resourceapi.DeviceAttribute {
	attrs := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
		"type": {StringValue: ptr.To(gpuDeviceType)},
	}
	if name, ok := gpuFullProductName[shortName]; ok {
		attrs["productName"] = resourceapi.DeviceAttribute{StringValue: ptr.To(name)}
	}
	if brand, ok := gpuBrand[shortName]; ok {
		attrs["brand"] = resourceapi.DeviceAttribute{StringValue: ptr.To(brand)}
	}
	if arch, ok := gpuArchitecture[shortName]; ok {
		attrs["architecture"] = resourceapi.DeviceAttribute{StringValue: ptr.To(arch)}
	}
	if cc, ok := gpuCudaComputeCapability[shortName]; ok {
		attrs["cudaComputeCapability"] = resourceapi.DeviceAttribute{VersionValue: ptr.To(cc)}
	}
	return attrs
}
