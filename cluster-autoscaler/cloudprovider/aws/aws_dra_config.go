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
	"sync"

	apiv1 "k8s.io/api/core/v1"
	coreoptions "k8s.io/autoscaler/cluster-autoscaler/core/options"
	"k8s.io/autoscaler/cluster-autoscaler/utils/kubernetes"
	v1lister "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/record"
	klog "k8s.io/klog/v2"
	"sigs.k8s.io/yaml"
)

// ConfigMap-backed alternative to the hardcoded GPU/MIG attribute maps in aws_dra.go and
// aws_dra_mig.go. Instead of compiling GPU capability data into the binary, this reads it
// from a ConfigMap so an operator can add a GPU model or fix a wrong MIG placement table by
// editing the ConfigMap, with no CA rebuild/redeploy.
//
// Follows the same pattern as the priority expander (expander/priority/priority.go): a
// lightweight client-go Reflector/Indexer-backed lister (utils/kubernetes.
// NewConfigMapListerForNamespace), no persistent parsed cache — every call re-reads the
// local watch-cache and re-parses the YAML — and a fail-safe fallback (log + not-found,
// never a crash) on any error. The lister's background watch means an edited ConfigMap
// takes effect on the very next call, typically within a second or two, not up to the
// hourly resync period.

const (
	// gpuConfigMapName is the ConfigMap holding GPU/MIG capability data, read from the
	// namespace CA itself runs in (opts.ConfigNamespace).
	gpuConfigMapName = "cluster-autoscaler-aws-dra-gpu-config"
	// fullGPUAttributesKey holds the Phase-1 (full-GPU) attribute map, one entry per EC2 GPU
	// short name. Mirrors gpuFullProductName/gpuBrand/gpuArchitecture/gpuCudaComputeCapability.
	fullGPUAttributesKey = "full-gpu-attributes.yaml"
	// migProfileTablesKey holds the Phase-2 (MIG) profile tables, one-to-one with the
	// hardcoded migProfileTables shape (short name -> list of memory variants).
	migProfileTablesKey = "mig-profile-tables.yaml"
)

// draGPUDataSource abstracts where Phase-1/Phase-2 GPU capability data comes from, so
// aws_dra.go/aws_dra_mig.go don't care whether it's compiled-in or ConfigMap-backed.
type draGPUDataSource interface {
	// fullGPUAttributes returns the full-GPU device attributes for an EC2 GPU short name.
	fullGPUAttributes(shortName string) (fullGPUAttrs, bool)
	// migVariants returns the MIG memory-variant table for an EC2 GPU short name.
	migVariants(shortName string) ([]migVariant, bool)
}

// fullGPUAttrs is the Phase-1 device-attribute set for one GPU short name. Fields are
// optional (empty string = attribute omitted), matching today's per-map independent lookups.
type fullGPUAttrs struct {
	ProductName           string `json:"productName,omitempty"`
	Brand                 string `json:"brand,omitempty"`
	Architecture          string `json:"architecture,omitempty"`
	CudaComputeCapability string `json:"cudaComputeCapability,omitempty"`
	// MemoryMiB overrides EC2's reported per-device GPU memory for the full-GPU capacity, for
	// GPU models where EC2's nominal figure differs from what the driver actually publishes
	// (e.g. reserved memory) — see the RTX PRO Server 6000 MIG table's whole.memory for a
	// verified example of this gap. Zero/omitted falls back to instanceType.GPUMemoryMiB.
	MemoryMiB int64 `json:"memoryMiB,omitempty"`
}

// configMapGPUDataSource is the real draGPUDataSource, backed by a ConfigMap.
type configMapGPUDataSource struct {
	configMapLister v1lister.ConfigMapNamespaceLister
	logRecorder     record.EventRecorder
	// lastWarned dedupes logWarning by (cm.ResourceVersion, msg): buildResourceSlicesFromTemplate
	// runs once per DRA-enabled node group per CA loop tick, so a persistently broken
	// ConfigMap would otherwise re-emit an Event + log line indefinitely, every tick, forever.
	lastWarned map[string]string
	warnMu     sync.Mutex
}

// newConfigMapGPUDataSource constructs a configMapGPUDataSource. logRecorder may be nil
// (e.g. in tests), in which case only klog warnings are emitted, no k8s Events.
func newConfigMapGPUDataSource(lister v1lister.ConfigMapNamespaceLister, logRecorder record.EventRecorder) *configMapGPUDataSource {
	return &configMapGPUDataSource{configMapLister: lister, logRecorder: logRecorder, lastWarned: map[string]string{}}
}

// logWarning emits an Event + klog warning for msg under key, but only once per distinct
// (key, cm.ResourceVersion, msg) combination — re-evaluating the same broken ConfigMap on
// every call does not re-log until the ConfigMap actually changes.
func (s *configMapGPUDataSource) logWarning(cm *apiv1.ConfigMap, key, reason, msg string) {
	resourceVersion := ""
	if cm != nil {
		resourceVersion = cm.ResourceVersion
	}
	dedupeKey := key + "@" + resourceVersion

	s.warnMu.Lock()
	alreadyWarned := s.lastWarned[dedupeKey] == msg
	if !alreadyWarned {
		s.lastWarned[dedupeKey] = msg
	}
	s.warnMu.Unlock()
	if alreadyWarned {
		return
	}

	if s.logRecorder != nil && cm != nil {
		s.logRecorder.Event(cm, apiv1.EventTypeWarning, reason, msg)
	}
	klog.Warning(msg)
}

func (s *configMapGPUDataSource) fullGPUAttributes(shortName string) (fullGPUAttrs, bool) {
	cm, err := s.configMapLister.Get(gpuConfigMapName)
	if err != nil {
		s.logWarning(nil, "not-found:"+fullGPUAttributesKey, "AwsDraGpuConfigNotFound", fmt.Sprintf("DRA GPU config map %s not found: %v", gpuConfigMapName, err))
		return fullGPUAttrs{}, false
	}
	raw, found := cm.Data[fullGPUAttributesKey]
	if !found {
		s.logWarning(cm, fullGPUAttributesKey, "AwsDraGpuConfigInvalid", fmt.Sprintf("DRA GPU config map %s missing key %s; ignoring", gpuConfigMapName, fullGPUAttributesKey))
		return fullGPUAttrs{}, false
	}
	var attrs map[string]fullGPUAttrs
	if err := yaml.Unmarshal([]byte(raw), &attrs); err != nil {
		s.logWarning(cm, fullGPUAttributesKey, "AwsDraGpuConfigInvalid", fmt.Sprintf("DRA GPU config map %s key %s: %v; ignoring", gpuConfigMapName, fullGPUAttributesKey, err))
		return fullGPUAttrs{}, false
	}
	a, ok := attrs[shortName]
	return a, ok
}

func (s *configMapGPUDataSource) migVariants(shortName string) ([]migVariant, bool) {
	cm, err := s.configMapLister.Get(gpuConfigMapName)
	if err != nil {
		s.logWarning(nil, "not-found:"+migProfileTablesKey, "AwsDraGpuConfigNotFound", fmt.Sprintf("DRA GPU config map %s not found: %v", gpuConfigMapName, err))
		return nil, false
	}
	raw, found := cm.Data[migProfileTablesKey]
	if !found {
		s.logWarning(cm, migProfileTablesKey, "AwsDraGpuConfigInvalid", fmt.Sprintf("DRA GPU config map %s missing key %s; ignoring", gpuConfigMapName, migProfileTablesKey))
		return nil, false
	}
	var tables map[string][]migVariant
	if err := yaml.Unmarshal([]byte(raw), &tables); err != nil {
		s.logWarning(cm, migProfileTablesKey, "AwsDraGpuConfigInvalid", fmt.Sprintf("DRA GPU config map %s key %s: %v; ignoring", gpuConfigMapName, migProfileTablesKey, err))
		return nil, false
	}
	rawVariants, ok := tables[shortName]
	if !ok {
		return nil, false
	}
	variants := make([]migVariant, 0, len(rawVariants))
	for i, v := range rawVariants {
		if err := v.validate(); err != nil {
			s.logWarning(cm, fmt.Sprintf("%s:%s:%d", migProfileTablesKey, shortName, i), "AwsDraGpuConfigInvalid", fmt.Sprintf("DRA GPU config map %s key %s: shortname %q variant %d: %v; skipping", gpuConfigMapName, migProfileTablesKey, shortName, i, err))
			continue
		}
		variants = append(variants, v)
	}
	if len(variants) == 0 {
		return nil, false
	}
	return variants, true
}

// noopGPUDataSource is used when no Kubernetes client is available to build a ConfigMap
// lister (e.g. some test/CLI paths); it always reports not-found, matching today's
// fail-safe behaviour for an unmapped GPU short name.
type noopGPUDataSource struct{}

func (noopGPUDataSource) fullGPUAttributes(string) (fullGPUAttrs, bool) { return fullGPUAttrs{}, false }
func (noopGPUDataSource) migVariants(string) ([]migVariant, bool)       { return nil, false }

// gpuDataSource is the process-wide source of GPU/MIG capability data consulted by
// aws_dra.go/aws_dra_mig.go. Defaults to noopGPUDataSource (fail-safe: no fabrication for
// any GPU short name) until initDraGPUDataSource runs; there is exactly one AWS cloud
// provider per process, so this mirrors the existing single-init pattern used elsewhere in
// this package (e.g. RegisterMetrics in aws_cloud_provider.go) rather than needing to be
// threaded through AwsManager/AwsNodeGroup as an explicit dependency.
var gpuDataSource draGPUDataSource = noopGPUDataSource{}

// initDraGPUDataSource wires up the ConfigMap-backed gpuDataSource from BuildAWS. Falls
// back to leaving gpuDataSource as the noop default if no KubeClient is available (e.g.
// some test/CLI paths), matching today's fail-safe behaviour for an unmapped GPU short name.
func initDraGPUDataSource(opts *coreoptions.AutoscalerOptions) {
	if opts.KubeClient == nil {
		klog.Warningf("DRA GPU ConfigMap data source disabled: no KubeClient available")
		return
	}
	var recorder record.EventRecorder
	if opts.AutoscalingKubeClients != nil {
		recorder = opts.AutoscalingKubeClients.Recorder
	}
	stopChannel := make(chan struct{}) // never closed, mirrors expander/factory's ConfigMap lister setup
	cmLister := kubernetes.NewConfigMapListerForNamespace(opts.KubeClient, stopChannel, opts.ConfigNamespace)
	gpuDataSource = newConfigMapGPUDataSource(cmLister.ConfigMaps(opts.ConfigNamespace), recorder)
}
