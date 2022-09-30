/*
Copyright 2022 The Kubernetes Authors.

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

package qos

// QoS Logic.
//- Annotation analysis for determining if/when we use QoS mode
//- Rounding:
//  - To next core
//  - With variance analysis support
//  - With modulo=N support
//- Interpolation:
//  - Fitting within the rounding rules.

import (
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/utils/annotations"
	"k8s.io/klog/v2"
	"math"
	"math/big"
	"strings"
)

func kindNeedsQoS(kind string) bool {
	switch strings.ToLower(kind) {
	case "deployment":
		return true
	case "statefulset":
		return true
	case "daemonset":
		return false
	default:
		klog.Warningf("Unknown kind %v, assuming it doesn't need QoS", kind)
	}
	return false
}

// NeedsQoS determines whether the entity of this kind and with these annotations
// needs QoS mode enabled.
func NeedsQoS(datadogExtensions annotations.DatadogExtensions) bool {
	kindBasedQoS := false
	if datadogExtensions.ObjectTypeSpecified {
		kindBasedQoS = kindNeedsQoS(datadogExtensions.ObjectType)
	}
	if datadogExtensions.QoSSpecified {
		return datadogExtensions.QoSEnable
	}
	return kindBasedQoS
}

// roundCoresToPolicy takes a basic core amount and rounds up to fit the QoS policy.
func roundCoresToPolicy(datadogExtensions annotations.DatadogExtensions, cores model.ResourceAmount) model.ResourceAmount {
	if datadogExtensions.QoSSpecified && datadogExtensions.QoSEnable {
		integerCores := 1000 * math.Ceil(float64(cores)/1000.0)
		if datadogExtensions.CoreDivisorSpecified && datadogExtensions.CoreDivisor > 1 {
			divisorMilliCores := datadogExtensions.CoreDivisor * 1000
			modulus := int(integerCores) % divisorMilliCores
			if modulus > 0 {
				return model.ResourceAmount(integerCores + float64(divisorMilliCores-modulus))
			}
			return model.ResourceAmount(integerCores)

		}
		return model.ResourceAmount(integerCores)

	}
	return cores
}

// RoundResourcesToPolicy applies the rounding policies in the given extensions to the given resources.
func RoundResourcesToPolicy(extensions annotations.DatadogExtensions, originalResources model.Resources) model.Resources {
	// First look for a RAM per core policy to dominate our calculations.
	memoryAmount, hasMemoryAmount := originalResources[model.ResourceMemory]
	cpuAmount, hasCpuAmount := originalResources[model.ResourceCPU]
	if extensions.RamPerCoreSpecified && hasMemoryAmount && hasCpuAmount {
		pointCpu := roundCoresToPolicy(extensions, cpuAmount)
		bytesPerCore, ok := extensions.RamPerCore.AsInt64()
		if !ok {
			klog.Warningf("Failed to int64-ify RAM Per Core value of %v", extensions.RamPerCore)
		} else {
			// Determine which point on the (x=cores, y=ram) line we have by either CPU or RAM, then take the
			// higher point.
			memQty := big.NewInt(int64(memoryAmount))
			bytesPerCoreInt := big.NewInt(bytesPerCore)
			memQty.Div(memQty, bytesPerCoreInt)
			// memQty is in full cores.  Convert to millicores for rounding.
			pointMem := roundCoresToPolicy(extensions, model.ResourceAmount(memQty.Int64()*1000))
			// pointCpu and our output CPU value has to be in millicores. Normalize to full cores here
			// for the higherPointMem calculation then go back and make it millicores in the result.
			higherPointCores := math.Max(float64(pointCpu), float64(pointMem))
			higherPointMem := (higherPointCores / 1000.0) * float64(bytesPerCoreInt.Int64())
			return model.Resources{
				model.ResourceCPU:    model.ResourceAmount(int64(higherPointCores)),
				model.ResourceMemory: model.ResourceAmount(int64(higherPointMem)),
			}
		}
	}

	// No Ram/Core policy (or not enough good data to use it), fall back to just rounding QoS cores.
	newResources := make(model.Resources)
	for resource, resourceAmount := range originalResources {
		if resource == model.ResourceCPU {
			newResources[resource] = roundCoresToPolicy(extensions, resourceAmount)
		} else {
			newResources[resource] = resourceAmount
		}
	}
	return newResources

}

// RoundResourceListToPolicy will call RoundResourcesToPolicy but unwrap/wrap a ResourceList back to its Resources.
func RoundResourceListToPolicy(extensions annotations.DatadogExtensions, originalResources v1.ResourceList) v1.ResourceList {
	convInputResources := model.Resources{
		model.ResourceCPU:    model.ResourceAmount(originalResources.Cpu().MilliValue()),
		model.ResourceMemory: model.ResourceAmount(originalResources.Memory().Value()),
	}
	rawResult := RoundResourcesToPolicy(extensions, convInputResources)
	return v1.ResourceList{
		v1.ResourceCPU:    *resource.NewScaledQuantity(int64(rawResult[model.ResourceCPU]), resource.Milli),
		v1.ResourceMemory: *resource.NewQuantity(int64(rawResult[model.ResourceMemory]), originalResources.Memory().Format),
	}
}
