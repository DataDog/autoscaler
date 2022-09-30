package annotations

import (
	"fmt"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/poc.autoscaling.k8s.io/v1alpha1"
	"strconv"
)

// Exported for external tests.
const (
	MetadataCopyPrefix            = "vpa.datadoghq.com/"
	AnnotationObjectType          = MetadataCopyPrefix + "type"
	AnnotationPreferredUpdateMode = MetadataCopyPrefix + "updateMode"
	AnnotationQosEnable           = MetadataCopyPrefix + "qosEnable"
	AnnotationCoreDivisor         = MetadataCopyPrefix + "coreDivisor"
	AnnotationRamPerCore          = MetadataCopyPrefix + "ramPerCore"
)

// DatadogExtensions holds all parsed metadata from an object (a workload or VPA).
// We may want to split up some of this into a custom QoS recommender.  That
// will likely get upstreamed.  But the datadog-api client is another matter.
type DatadogExtensions struct {
	ObjectTypeSpecified bool
	ObjectType          string
	UpdateModeSpecified bool
	UpdateMode          v1alpha1.UpdateMode
	QoSSpecified        bool
	QoSEnable           bool
	// CoreDivisor only has an effect if QoS is explicitly or implicitly (by type) enabled.
	CoreDivisorSpecified bool
	CoreDivisor          int
	RamPerCoreSpecified  bool
	RamPerCore           resource.Quantity
}

func ParseDatadogExtensions(annotations map[string]string) (DatadogExtensions, error) {
	result := DatadogExtensions{
		ObjectTypeSpecified:  false,
		ObjectType:           "",
		UpdateModeSpecified:  false,
		UpdateMode:           v1alpha1.UpdateModeOff,
		QoSSpecified:         false,
		QoSEnable:            false,
		CoreDivisorSpecified: false,
		CoreDivisor:          1,
		RamPerCoreSpecified:  false,
		RamPerCore:           *resource.NewQuantity(0, resource.DecimalSI),
	}
	var lastError error
	if v, ok := annotations[AnnotationObjectType]; ok {
		switch v {
		case "Deployment":
			result.ObjectType = v
			result.ObjectTypeSpecified = true
		case "StatefulSet":
			result.ObjectType = v
			result.ObjectTypeSpecified = true
		case "DaemonSet":
			result.ObjectType = v
			result.ObjectTypeSpecified = true
		}
		result.ObjectTypeSpecified = false
	}
	if v, ok := annotations[AnnotationQosEnable]; ok {
		val, err := strconv.ParseBool(v)
		if err == nil {
			result.QoSSpecified = true
			result.QoSEnable = val
		} else {
			lastError = err
		}
	}
	if v, ok := annotations[AnnotationPreferredUpdateMode]; ok {
		switch v {
		case string(v1alpha1.UpdateModeOff):
			result.UpdateModeSpecified = true
			result.UpdateMode = v1alpha1.UpdateMode(v)
		case string(v1alpha1.UpdateModeInitial):
			result.UpdateModeSpecified = true
			result.UpdateMode = v1alpha1.UpdateMode(v)
		case string(v1alpha1.UpdateModeRecreate):
			result.UpdateModeSpecified = true
			result.UpdateMode = v1alpha1.UpdateMode(v)
		case string(v1alpha1.UpdateModeAuto):
			result.UpdateModeSpecified = true
			result.UpdateMode = v1alpha1.UpdateMode(v)
		default:
			err := fmt.Errorf("invalid UpdateMode: %v", v)
			lastError = err
		}
	}
	if v, ok := annotations[AnnotationCoreDivisor]; ok {
		value, err := strconv.Atoi(v)
		if err == nil {
			result.CoreDivisorSpecified = true
			result.CoreDivisor = value
		} else {
			lastError = err
		}
	}
	if v, ok := annotations[AnnotationRamPerCore]; ok {
		value, err := resource.ParseQuantity(v)
		if err == nil {
			result.RamPerCoreSpecified = true
			result.RamPerCore = value
		} else {
			lastError = err
		}
	}

	return result, lastError
}
