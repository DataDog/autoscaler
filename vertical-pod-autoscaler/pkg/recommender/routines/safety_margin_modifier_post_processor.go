/*
Copyright 2023 The Kubernetes Authors.

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

package routines

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
	"k8s.io/klog/v2"
)

// SafetyMarginModifierPostProcessor is a post processor that, when enabled, allow a user to override the default safety margin
// added by the recommender via an annotation on the VPA object. The user can specify which resource is impacted, what function is
// used (e.g.: linear, affine, log, exponential), and the parameters of that function.
type SafetyMarginModifierPostProcessor struct {
	DefaultSafetyMarginFactor float64
}

type modifierFunction int64

const (
	undefined modifierFunction = iota
	linear
	log
	affine
	exponential
)

type safetyMarginFunction struct {
	Function   modifierFunction
	Parameters []float64
}

type safetyMarginModifier map[v1.ResourceName]safetyMarginFunction

const (
	vpaPostProcessorSafetyMarginModifierSuffix = "_safetyMarginModifier"
)

var _ RecommendationPostProcessor = &SafetyMarginModifierPostProcessor{}

// Process applies the Resource Ratio post-processing to the recommendation.
func (r *SafetyMarginModifierPostProcessor) Process(vpa *model.Vpa, recommendation *vpa_types.RecommendedPodResources, policy *vpa_types.PodResourcePolicy) *vpa_types.RecommendedPodResources {
	updatedRecommendation := recommendation.DeepCopy()
	modifiers := readSafetyMarginModifierFromVPAAnnotations(vpa)

	for _, containerRec := range updatedRecommendation.ContainerRecommendations {
		modifier, found := modifiers[containerRec.ContainerName]

		if !found {
			continue
		}
		klog.Infof("Apply safety marging modifier on container %s in vpa %s/%s", containerRec.ContainerName, vpa.ID.Namespace, vpa.ID.VpaName)

		err := r.applyModifier(vpa, containerRec.ContainerName, containerRec.LowerBound, modifier)
		if err != nil {
			klog.Errorf("Failed to apply SafetyMarginModifiePostProcessor to vpa %s/%s due to error: %#v", err)
			return recommendation
		}

		err = r.applyModifier(vpa, containerRec.ContainerName, containerRec.Target, modifier)
		if err != nil {
			klog.Errorf("Failed to apply SafetyMarginModifiePostProcessor to vpa %s/%s due to error: %#v", err)
			return recommendation
		}

		err = r.applyModifier(vpa, containerRec.ContainerName, containerRec.UpperBound, modifier)
		if err != nil {
			klog.Errorf("Failed to apply SafetyMarginModifiePostProcessor to vpa %s/%s due to error: %#v", err)
			return recommendation
		}

		err = r.applyModifier(vpa, containerRec.ContainerName, containerRec.UncappedTarget, modifier)
		if err != nil {
			klog.Errorf("Failed to apply SafetyMarginModifiePostProcessor to vpa %s/%s due to error: %#v", err)
			return recommendation
		}
	}

	return updatedRecommendation
}

func (r *SafetyMarginModifierPostProcessor) applyModifier(vpa *model.Vpa, name string, containerRec v1.ResourceList, modifier safetyMarginModifier) error {
	for resourceName, rec := range containerRec {
		resourceModifier, found := modifier[resourceName]
		if !found {
			continue
		}

		var newRec float64

		// CPU requests are expressed in milli cores, which impacts the modification brough by
		// the Log and Exponential modifiers. If the resource is CPU, scale it by 1000.
		recCopy := rec.DeepCopy()
		if resourceName == v1.ResourceCPU {
			recCopy.SetScaled(rec.MilliValue(), 0)
		}

		switch resourceModifier.Function {
		case linear:
			if len(resourceModifier.Parameters) != 1 {
				return fmt.Errorf("skipping %s safety margin modifier from container %s in vpa %s/%s: linear modifier requires 1 parameter, %d given", resourceName, name, vpa.ID.Namespace, vpa.ID.VpaName, len(resourceModifier.Parameters))
			}
			newRec = linearModifier(recCopy, r.DefaultSafetyMarginFactor, resourceModifier.Parameters[0])
		case affine:
			if len(resourceModifier.Parameters) != 2 {
				return fmt.Errorf("skipping %s safety margin modifier from container %s in vpa %s/%s: affine modifier requires 2 parameters, %d given", resourceName, name, vpa.ID.Namespace, vpa.ID.VpaName, len(resourceModifier.Parameters))
			}
			newRec = affineModifier(recCopy, r.DefaultSafetyMarginFactor, resourceModifier.Parameters[0], resourceModifier.Parameters[1])
		case log:
			if len(resourceModifier.Parameters) != 1 {
				return fmt.Errorf("skipping %s safety margin modifier from container %s in vpa %s/%s: log modifier requires 1 parameter, %d given", resourceName, name, vpa.ID.Namespace, vpa.ID.VpaName, len(resourceModifier.Parameters))
			}
			newRec = logModifier(recCopy, r.DefaultSafetyMarginFactor, resourceModifier.Parameters[0])
		case exponential:
			if len(resourceModifier.Parameters) != 2 {
				return fmt.Errorf("skipping %s safety margin modifier from container %s in vpa %s/%s: exponential modifier requires 2 parameters, %d given", resourceName, name, vpa.ID.Namespace, vpa.ID.VpaName, len(resourceModifier.Parameters))
			}
			newRec = exponentialModifier(recCopy, r.DefaultSafetyMarginFactor, resourceModifier.Parameters[0], resourceModifier.Parameters[1])
		case undefined:
			continue
		default:
			return fmt.Errorf("skipping %s safety margin modifier for container %s in vpa %s/%s: specified modifier is not valid", resourceName, name, vpa.ID.Namespace, vpa.ID.VpaName)
		}

		if resourceName == v1.ResourceCPU {
			containerRec[resourceName] = *resource.NewMilliQuantity(int64(newRec), resource.DecimalSI)
		} else {
			containerRec[resourceName] = *resource.NewQuantity(int64(newRec), resource.DecimalSI)
		}
	}
	return nil
}

// Undo default safety margin before applying a custom linear safety margin (a * baseRec)
func linearModifier(rec resource.Quantity, defaultSafetyMargin, slope float64) float64 {
	return float64(rec.Value()) / defaultSafetyMargin * slope
}

// Undo default safety margin before applying a custom affine safety margin (a * baseRec + b)
func affineModifier(rec resource.Quantity, defaultSafetyMargin, constant, slope float64) float64 {
	return float64(rec.Value())/defaultSafetyMargin*slope + constant
}

// Undo default safety margin before applying a custom logarithmic safety margin (baseRec + a * log10(baseRec))
func logModifier(rec resource.Quantity, defaultSafetyMargin, factor float64) float64 {
	rawRec := float64(rec.Value()) / defaultSafetyMargin
	return rawRec + math.Log10(rawRec)*factor
}

// Undo default safety margin before applying a custom exponential safety margin (baseRec + a * baseRec^b)
func exponentialModifier(rec resource.Quantity, defaultSafetyMargin, exponent, factor float64) float64 {
	rawRec := float64(rec.Value()) / defaultSafetyMargin
	return rawRec + math.Pow(rawRec, exponent)*factor
}

func readSafetyMarginModifierFromVPAAnnotations(vpa *model.Vpa) map[string]safetyMarginModifier {
	modifiers := map[string]safetyMarginModifier{}
	for key, value := range vpa.Annotations {
		containerName := extractContainerName(key, vpaPostProcessorPrefix, vpaPostProcessorSafetyMarginModifierSuffix)
		if containerName == "" {
			continue
		}

		safetyMarginModifier := safetyMarginModifier{}
		if err := json.Unmarshal([]byte(value), &safetyMarginModifier); err != nil {
			klog.Warningf("Skipping safety margin modifier definition '%s' for container '%s' in vpa %s/%s due to bad format, error:%#v", value, containerName, vpa.ID.Namespace, vpa.ID.VpaName, err)
			continue
		}

		// Provide a wildcard modifier if CPU and Memory modifiers are the same
		if value, found := safetyMarginModifier["*"]; found {
			delete(safetyMarginModifier, "*")
			safetyMarginModifier[v1.ResourceCPU] = value
			safetyMarginModifier[v1.ResourceMemory] = value
		}

		modifiers[containerName] = safetyMarginModifier
	}
	return modifiers
}

func (f modifierFunction) String() string {
	switch f {
	case linear:
		return "linear"
	case log:
		return "log"
	case affine:
		return "affine"
	case exponential:
		return "exponential"
	default:
		return "undefined"
	}
}

func (f modifierFunction) MarshalJSON() ([]byte, error) {
	return json.Marshal(f.String())
}

func (f *modifierFunction) UnmarshalJSON(data []byte) (err error) {
	var functionString string
	if err := json.Unmarshal(data, &functionString); err != nil {
		return err
	}
	functionString = strings.ToLower(functionString)

	switch functionString {
	case "linear":
		*f = linear
	case "log":
		*f = log
	case "affine":
		*f = affine
	case "exponential":
		*f = exponential
	default:
		*f = undefined
		return fmt.Errorf("cannot unmarshall string into ModifierFunction: %s is not a valid function name", functionString)
	}
	return nil
}
