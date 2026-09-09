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
// attribute data operator-editable without a CA rebuild/redeploy. Upstream tracks proper
// DRA scale-from-zero support in https://github.com/kubernetes/autoscaler/issues/7799.

// gpuDeviceType is the value of the "type" device attribute, matching what the NVIDIA
// DRA driver emits at runtime. draPluginManagedLabelKey and nvidiaDRADriverName are
// defined in aws_cloud_provider.go, shared with the GetNodeGpuConfig readiness opt-out.
const gpuDeviceType = "gpu"

// GPU attribute data is read from a ConfigMap at runtime (see aws_dra_config.go,
// draGPUDataSource), keyed on the EC2 GpuInfo short name, since EC2 only exposes the short
// name and per-device memory — everything else the driver publishes via NVML must be
// reproduced there for CEL selectors to match during scale-up simulation.

// buildResourceSlicesFromTemplate fabricates the DRA ResourceSlices for a template node,
// reproducing what the NVIDIA DRA driver would publish on the real node. It returns nil
// for node groups that are not DRA-enabled or have no GPUs, leaving non-DRA behaviour
// unchanged.
func buildResourceSlicesFromTemplate(node *apiv1.Node, instanceType *InstanceType) []*resourceapi.ResourceSlice {
	if node == nil || instanceType == nil {
		return nil
	}
	if node.Labels[draPluginManagedLabelKey] != "true" || instanceType.GPU == 0 {
		return nil
	}
	driver := nvidiaDRADriverName

	// Log the observed GPU short name so the correct ConfigMap key can be discovered from
	// CA logs (EC2 short names are not always known ahead of time).
	klog.V(4).Infof("DRA: building ResourceSlices for node group GPU %q (%s, driver %s)", instanceType.GPUShortName, instanceType.InstanceType, driver)

	// Empty short name + memory means --aws-use-static-instance-list is in use (EC2 API
	// unreachable). Warn and keep the degraded, attribute-less slice rather than returning
	// nil, so scale-from-zero still works for claims that don't constrain on attributes.
	if instanceType.GPUShortName == "" && instanceType.GPUMemoryMiB == 0 {
		klog.Warningf("DRA enabled for node group with GPU instance type %s but GPUShortName/GPUMemoryMiB are unset (likely --aws-use-static-instance-list); fabricated ResourceSlices will be missing attributes and MIG groups will get none", instanceType.InstanceType)
	}

	// A GPU is MIG-capable, and thus advertises partitionable devices (see aws_dra_mig.go),
	// exactly when the ConfigMap-backed data source has a MIG profile table for its short
	// name — the NVIDIA DRA plugin runs with DynamicMIG=true uniformly, so any MIG-capable
	// GPU publishes MIG slices at runtime. A GPU with no table is not MIG-capable and gets
	// full-GPU slices, the only path available for it.
	if _, ok := gpuDataSource.migVariants(instanceType.GPUShortName); ok {
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

// gpuDeviceAttributes returns the device attributes for a looked-up fullGPUAttrs. "type" is
// always present; richer attributes are added only if found. Runtime-only attributes NVML
// publishes (driverVersion, uuid, pciBusID, ...) are omitted — unknowable pre-scale.
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
