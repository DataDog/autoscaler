package instancepools

import (
	"regexp"
	"strings"

	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"
)

const (
	// LabelPrefix denotes a label value pair that a node from this instance pool is expected to advertise.
	// For example, cluster-autoscaler/node-template/label/foo=bar will inform the CA a node from this pool is
	// expected to have a label foo=bar
	LabelPrefix = "cluster-autoscaler/node-template/label/"

	// TaintPrefix denotes a taint value that a node from this instance pool is expected to advertise.
	// For example, cluster-autoscaler/node-template/taint/node=default:NoSchedule will inform the CA a node from this
	// pool is expected to have a taint node=default:NoSchedule
	TaintPrefix = "cluster-autoscaler/node-template/taint/"

	// ResourcesPrefix denotes a resource value that a node from this instance pool is expected to advertise.
	// For example, cluster-autoscaler/node-template/resources/cpu=8000m will inform the CA a node from this pool is
	// expected to advertise a cpu resource with capacity 8000m
	ResourcesPrefix = "cluster-autoscaler/node-template/resources/"

	// OCI does not allow period '.' literals in tag keys. The special character '~2' is used instead, with inspiration
	// from RFC6901. This follows a similar approach to the Azure provider's escaping of '_'.
	periodSub = "~2"
)

// taintValidationRegex defines the required format for value/effects in a tag value.
// The tag value must be in the format <value>:NoSchedule|NoExecute|PreferNoSchedule
var taintValidationRegex = regexp.MustCompile("(.*):(?:NoSchedule|NoExecute|PreferNoSchedule)")

// NodeTemplater extracts node template attributes from OCI instance tags.
// The defined-tags nodeTemplateNamespace is resolved once at construction time.
type NodeTemplater struct {
	nodeTemplateNamespace string
}

// NodeTemplateAttributes holds the node template attributes extracted from OCI instance tags.
type NodeTemplateAttributes struct {
	Labels    map[string]string
	Taints    []apiv1.Taint
	Resources map[string]*resource.Quantity
}

// NewNodeTemplater constructs a NodeTemplater that looks up defined tags under
// the given nodeTemplateNamespace.
func NewNodeTemplater(nodeTemplateNamespace string) NodeTemplater {
	return NodeTemplater{nodeTemplateNamespace: nodeTemplateNamespace}
}

// ExtractNodeTemplateAttributes extracts all node template attributes (labels, taints, resources,
// and autoscaling options) from the provided freeform and defined tags.
func (t NodeTemplater) ExtractNodeTemplateAttributes(freeformTags map[string]string, definedTags map[string]map[string]string) NodeTemplateAttributes {
	return NodeTemplateAttributes{
		Labels:    t.extractLabels(freeformTags, definedTags),
		Taints:    t.extractTaints(freeformTags, definedTags),
		Resources: t.extractResources(freeformTags, definedTags),
	}
}

func (t NodeTemplater) extractLabels(freeformTags map[string]string, definedTags map[string]map[string]string) map[string]string {
	labels := make(map[string]string)
	if namespaceTags, ok := definedTags[t.nodeTemplateNamespace]; ok {
		for k, v := range extractLabelsFromMap(namespaceTags) {
			labels[k] = v
		}
	}
	for k, v := range extractLabelsFromMap(freeformTags) {
		labels[k] = v
	}
	return labels
}

func (t NodeTemplater) extractResources(freeformTags map[string]string, definedTags map[string]map[string]string) map[string]*resource.Quantity {
	resources := make(map[string]*resource.Quantity)
	if namespaceTags, ok := definedTags[t.nodeTemplateNamespace]; ok {
		for k, v := range extractResourcesFromMap(namespaceTags) {
			resources[k] = v
		}
	}
	for k, v := range extractResourcesFromMap(freeformTags) {
		resources[k] = v
	}
	return resources
}

func (t NodeTemplater) extractTaints(freeformTags map[string]string, definedTags map[string]map[string]string) []apiv1.Taint {
	var taints []apiv1.Taint
	if namespaceTags, ok := definedTags[t.nodeTemplateNamespace]; ok {
		taints = append(taints, extractTaintsFromMap(namespaceTags)...)
	}
	taints = append(taints, extractTaintsFromMap(freeformTags)...)
	return taints
}

func extractLabelsFromMap(freeformTags map[string]string) map[string]string {
	labels := make(map[string]string)
	for key, value := range freeformTags {
		if !strings.HasPrefix(key, LabelPrefix) {
			continue
		}
		labelKey := strings.ReplaceAll(key[len(LabelPrefix):], periodSub, ".")
		labels[labelKey] = value
	}
	return labels
}

func extractResourcesFromMap(freeformTags map[string]string) map[string]*resource.Quantity {
	resources := make(map[string]*resource.Quantity)
	for key, value := range freeformTags {
		if !strings.HasPrefix(key, ResourcesPrefix) {
			continue
		}
		resourceKey := strings.ReplaceAll(key[len(ResourcesPrefix):], periodSub, ".")
		quantity, err := resource.ParseQuantity(value)
		if err != nil {
			klog.Warningf("Failed to parse resource quantity '%s' for resource '%s': %v", value, resourceKey, err)
			continue
		}
		resources[resourceKey] = &quantity
	}
	return resources
}

func extractTaintsFromMap(freeformTags map[string]string) []apiv1.Taint {
	taints := make([]apiv1.Taint, 0)
	for key, value := range freeformTags {
		if !strings.HasPrefix(key, TaintPrefix) {
			continue
		}
		taintKey := strings.ReplaceAll(key[len(TaintPrefix):], periodSub, ".")
		if taintValidationRegex.MatchString(value) {
			values := strings.SplitN(value, ":", 2)
			if len(values) > 1 {
				taints = append(taints, apiv1.Taint{
					Key:    taintKey,
					Value:  values[0],
					Effect: apiv1.TaintEffect(values[1]),
				})
			}
		}
	}
	return taints
}
