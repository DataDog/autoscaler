/*
Copyright 2021-2024 Oracle and/or its affiliates.
*/

package common

import (
	"fmt"

	oke "k8s.io/autoscaler/cluster-autoscaler/cloudprovider/oci/vendor-internal/github.com/oracle/oci-go-sdk/v65/containerengine"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider/oci/vendor-internal/github.com/oracle/oci-go-sdk/v65/core"
)

// TagsGetter returns the oci tags for the pool.
type TagsGetter interface {
	GetNodePoolFreeformTags(*oke.NodePool) (map[string]string, error)
	GetNodePoolDefinedTags(*oke.NodePool) (map[string]map[string]string, error)
	GetInstancePoolFreeformTags(*core.InstancePool) (map[string]string, error)
	GetInstancePoolDefinedTags(*core.InstancePool) (map[string]map[string]string, error)
}

// TagsGetterImpl is the implementation to fetch the oci tags for the pool.
type TagsGetterImpl struct{}

// CreateTagsGetter creates a new oci tags getter.
func CreateTagsGetter() TagsGetter {
	return &TagsGetterImpl{}
}

// GetNodePoolFreeformTags returns the FreeformTags for the nodepool
func (tgi *TagsGetterImpl) GetNodePoolFreeformTags(np *oke.NodePool) (map[string]string, error) {
	return np.FreeformTags, nil
}

// GetNodePoolDefinedTags returns the DefinedTags for the nodepool
func (tgi *TagsGetterImpl) GetNodePoolDefinedTags(np *oke.NodePool) (map[string]map[string]string, error) {
	return definedTagsFromMap(np.DefinedTags)
}

// GetInstancePoolFreeformTags returns the FreeformTags for the instance pool
func (tgi *TagsGetterImpl) GetInstancePoolFreeformTags(pool *core.InstancePool) (map[string]string, error) {
	return pool.FreeformTags, nil
}

// GetInstancePoolDefinedTags returns the DefinedTags for the instance pool
func (tgi *TagsGetterImpl) GetInstancePoolDefinedTags(pool *core.InstancePool) (map[string]map[string]string, error) {
	return definedTagsFromMap(pool.DefinedTags)
}

// definedTagsFromMap converts a map[string]map[string]interface{} to map[string]map[string]string.
// In practice, the tag values returned in defined tags should always be expressible as a string
func definedTagsFromMap(in map[string]map[string]interface{}) (map[string]map[string]string, error) {
	definedTags := make(map[string]map[string]string)
	for ns, tags := range in {
		definedTags[ns] = make(map[string]string)
		for k, v := range tags {
			switch v.(type) {
			case string:
				definedTags[ns][k] = v.(string)
			case int, int32, int64:
				definedTags[ns][k] = fmt.Sprintf("%d", v)
			case float32, float64:
				definedTags[ns][k] = fmt.Sprintf("%f", v)
			case bool:
				definedTags[ns][k] = fmt.Sprintf("%t", v)
			default:
				return nil, fmt.Errorf("expected string-compatible value for tag %s/%s, got %T (%v)", ns, k, v, v)
			}
		}
	}
	return definedTags, nil
}
