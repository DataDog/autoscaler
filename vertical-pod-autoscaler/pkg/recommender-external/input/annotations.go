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

const (
	// VpaAnnotationsDomain is the prefix for each vpa annotations.
	// TODO: There's nothing datadog specific here, but it's a custom recommender, so we need to namespace the annotations somehow
	VpaAnnotationsDomain = "vpa.datadoghq.com/"
	// VpaAnnotationPrefix is the prefix for all annotations.
	// TODO: Rework that once we support a third resource.
	VpaAnnotationPrefix = VpaAnnotationsDomain + "/metric-"
	// CpuRecommendationQuery is the prefix for cpu metrics
	CpuRecommendationQuery = VpaAnnotationPrefix + "cpu-"
	// MemoryRecommendationQuery is the prefix for memory metrics.
	MemoryRecommendationQuery = VpaAnnotationPrefix + "memory-"
)
