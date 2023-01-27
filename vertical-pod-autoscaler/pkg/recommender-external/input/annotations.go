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

package input

import (
	"fmt"
	"strings"

	"k8s.io/klog/v2"

	upstream_model "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
)

const (
	// VpaAnnotationsDomain is the prefix for each vpa annotations.
	// TODO: There's nothing datadog specific here, but it's a custom recommender, so we need to namespace the annotations somehow
	VpaAnnotationsDomain = "vpa.datadoghq.com"

	// VpaAnnotationPrefix is the prefix for all annotations.
	// The full annotation looks like `vpa.datadoghq.com/metric-<resource>-<container>`
	// TODO: Rework that once we support a third resource.
	VpaAnnotationPrefix = VpaAnnotationsDomain + "/metric-"
)

var (
	// SupportedResources gives us the list of resources we can handle and configure using annotaitons.
	SupportedResources = []upstream_model.ResourceName{upstream_model.ResourceCPU, upstream_model.ResourceMemory}
)

// ContainersToResourcesAndMetrics maps a contaienr name to associated resource names and metrics.
type ContainersToResourcesAndMetrics map[string]map[upstream_model.ResourceName]string

// NewContainersToResourcesAndMetrics creates a ContainersToResourcesAndMetrics
func NewContainersToResourcesAndMetrics() ContainersToResourcesAndMetrics {
	return make(ContainersToResourcesAndMetrics)
}

// addMetric adds a container, resource and associated metric
func (c ContainersToResourcesAndMetrics) addMetric(container string, resource upstream_model.ResourceName, metric string) {
	if _, ok := c[container]; !ok {
		c[container] = make(map[upstream_model.ResourceName]string)
	}
	c[container][resource] = metric
}

// parseAnnotationKV parses a container, resource to metric annotation
func (c ContainersToResourcesAndMetrics) parseAnnotationKV(k, v string) error {
	container, resource, err := parseAnnotationKey(k)
	if err != nil {
		return err
	}
	c.addMetric(container, resource, v)
	return nil
}

// AnnotationKey creates an annotation key from a container and resource name.
func AnnotationKey(container string, cpu upstream_model.ResourceName) string {
	return VpaAnnotationPrefix + string(cpu) + "-" + container
}

// parseAnnotationKey parses a container, resource to metric annotation
func parseAnnotationKey(k string) (container string, resource upstream_model.ResourceName, err error) {
	// Here we have vpa.datadoghq.com/metric-* and we expect * to match <resource>-<container>

	a := strings.TrimPrefix(k, VpaAnnotationPrefix)

	// Now we expect to have: `<resource>-<container>`

	for _, resource = range SupportedResources {
		prefix := string(resource + "-")
		if strings.HasPrefix(a, prefix) {
			container = strings.TrimPrefix(a, prefix)
			// Then: `<container>`

			return container, resource, nil
		}
	}

	return "", "", fmt.Errorf("can't recognize %s", k)
}

// isVpaExternalMetricAnnotation returns true if an annotation configures a metric for a container and resource
func isVpaExternalMetricAnnotation(annotation string) bool {
	return strings.HasPrefix(annotation, VpaAnnotationPrefix)
}

// GetVpaExternalMetrics returns the map of containers, resource to metrics
func GetVpaExternalMetrics(annotations map[string]string) ContainersToResourcesAndMetrics {
	c := NewContainersToResourcesAndMetrics()
	for k, v := range annotations {
		if isVpaExternalMetricAnnotation(k) {
			klog.V(6).Infof("Found annotation with relevant prefix %s:%s", k, v)
			err := c.parseAnnotationKV(k, v)
			if err != nil {
				klog.V(2).ErrorS(err, fmt.Sprintf("Can't parse %s:%s", k, v))
			}
		}
	}
	if len(c) > 0 {
		klog.V(6).Infof("Found %+v", c)
	}
	return c
}
