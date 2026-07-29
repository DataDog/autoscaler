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
	"os"
	"testing"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1lister "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/yaml"
)

// TestMain seeds the package-level gpuDataSource with a fixture before any test runs, so
// aws_dra_test.go/aws_dra_mig_allocator_test.go's calls to buildResourceSlicesFromTemplate/
// buildMIGResourceSlices (unchanged from the hardcoded-map branch — no explicit data-source
// parameter) exercise the same values the old compiled-in maps carried.
func TestMain(m *testing.M) {
	gpuDataSource = testGPUDataSource()
	os.Exit(m.Run())
}

// fakeGPUDataSource is an in-memory draGPUDataSource test double, used both as the TestMain
// fixture above and directly by the ConfigMap-lister tests below.
type fakeGPUDataSource struct {
	attrs map[string]fullGPUAttrs
	migs  map[string][]migVariant
}

func (f fakeGPUDataSource) fullGPUAttributes(shortName string) (fullGPUAttrs, bool) {
	a, ok := f.attrs[shortName]
	return a, ok
}

func (f fakeGPUDataSource) migVariants(shortName string) ([]migVariant, bool) {
	v, ok := f.migs[shortName]
	return v, ok
}

// testGPUDataSource returns a fixture with the same literal values the hardcoded maps
// used to carry, covering every GPU short name the existing tests exercise. B200 and L4 are
// deliberately absent from their respective tables so the "unknown SKU" tests still cover
// the not-found fallback path.
func testGPUDataSource() fakeGPUDataSource {
	return fakeGPUDataSource{
		attrs: map[string]fullGPUAttrs{
			"A10G": {ProductName: "NVIDIA A10G", Brand: "Nvidia", Architecture: "Ampere", CudaComputeCapability: "8.6.0"},
			"H100": {ProductName: "NVIDIA H100 80GB HBM3", Brand: "Nvidia", Architecture: "Hopper", CudaComputeCapability: "9.0.0"},
		},
		migs: map[string][]migVariant{
			// VERIFIED: NVIDIA RTX PRO 6000 Blackwell Server Edition (AWS g7e).
			rtxPro6000ShortName: {{
				GPUMemoryMiB:          98304,
				ProductName:           "NVIDIA RTX PRO 6000 Blackwell Server Edition",
				Brand:                 "Nvidia",
				Architecture:          "Blackwell",
				CudaComputeCapability: "12.0.0",
				MemorySlices:          12,
				Whole:                 engineCounts{Multiprocessors: 188, CopyEngines: 4, Decoders: 4, Encoders: 4, JPEGEngines: 4, OFAEngines: 1, Memory: "95Gi"},
				Profiles: []migProfile{
					{Name: "1g.24gb", ProfileID: 14, MemorySlices: 3, Placements: []int{0, 3, 6, 9}, Engines: engineCounts{Multiprocessors: 46, CopyEngines: 1, Decoders: 1, Encoders: 1, JPEGEngines: 1, OFAEngines: 0, Memory: "24192Mi"}},
					{Name: "2g.48gb", ProfileID: 5, MemorySlices: 6, Placements: []int{0, 6}, Engines: engineCounts{Multiprocessors: 94, CopyEngines: 2, Decoders: 2, Encoders: 2, JPEGEngines: 2, OFAEngines: 0, Memory: "48512Mi"}},
					{Name: "4g.96gb", ProfileID: 0, MemorySlices: 12, Placements: []int{0}, Engines: engineCounts{Multiprocessors: 188, CopyEngines: 4, Decoders: 4, Encoders: 4, JPEGEngines: 4, OFAEngines: 1, Memory: "95Gi"}},
				},
			}},
			// UNVERIFIED: NVIDIA A100 (AWS p4d/p4de). Placeholder structure.
			"A100": {{
				GPUMemoryMiB:          40960,
				ProductName:           "NVIDIA A100-SXM4-40GB",
				Brand:                 "Nvidia",
				Architecture:          "Ampere",
				CudaComputeCapability: "8.0.0",
				MemorySlices:          8,
				Whole:                 engineCounts{Multiprocessors: 108, CopyEngines: 7, Decoders: 5, Encoders: 0, JPEGEngines: 1, OFAEngines: 1, Memory: "40Gi"},
				Profiles: []migProfile{
					{Name: "1g.5gb", ProfileID: 19, MemorySlices: 1, Placements: []int{0, 1, 2, 3, 4, 5, 6}, Engines: engineCounts{Multiprocessors: 14, CopyEngines: 1, Memory: "5Gi"}},
					{Name: "2g.10gb", ProfileID: 14, MemorySlices: 2, Placements: []int{0, 2, 4}, Engines: engineCounts{Multiprocessors: 28, CopyEngines: 2, Decoders: 1, Memory: "10Gi"}},
					{Name: "3g.20gb", ProfileID: 9, MemorySlices: 4, Placements: []int{0, 4}, Engines: engineCounts{Multiprocessors: 42, CopyEngines: 3, Decoders: 2, Memory: "20Gi"}},
					{Name: "7g.40gb", ProfileID: 0, MemorySlices: 8, Placements: []int{0}, Engines: engineCounts{Multiprocessors: 98, CopyEngines: 7, Decoders: 5, JPEGEngines: 1, OFAEngines: 1, Memory: "40Gi"}},
				},
			}},
		},
	}
}

// newTestConfigMapLister builds a v1lister.ConfigMapNamespaceLister seeded directly from an
// indexer (no reflector/watch), for tests that exercise configMapGPUDataSource itself.
func newTestConfigMapLister(t *testing.T, namespace string, cms ...*apiv1.ConfigMap) v1lister.ConfigMapNamespaceLister {
	t.Helper()
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	for _, cm := range cms {
		if err := indexer.Add(cm); err != nil {
			t.Fatalf("failed to seed fake configmap indexer: %v", err)
		}
	}
	return v1lister.NewConfigMapLister(indexer).ConfigMaps(namespace)
}

func testConfigMap(namespace string, data map[string]string) *apiv1.ConfigMap {
	return &apiv1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: gpuConfigMapName, Namespace: namespace},
		Data:       data,
	}
}

func TestConfigMapGPUDataSource_MissingConfigMap(t *testing.T) {
	src := newConfigMapGPUDataSource(newTestConfigMapLister(t, "kube-system"), nil)

	_, ok := src.fullGPUAttributes("A10G")
	if ok {
		t.Fatal("expected not-found when the ConfigMap doesn't exist")
	}
	if _, ok := src.migVariants(rtxPro6000ShortName); ok {
		t.Fatal("expected not-found when the ConfigMap doesn't exist")
	}
}

func TestConfigMapGPUDataSource_MissingKey(t *testing.T) {
	cm := testConfigMap("kube-system", map[string]string{"unrelated-key": "value"})
	src := newConfigMapGPUDataSource(newTestConfigMapLister(t, "kube-system", cm), nil)

	if _, ok := src.fullGPUAttributes("A10G"); ok {
		t.Fatal("expected not-found when full-gpu-attributes.yaml key is missing")
	}
	if _, ok := src.migVariants(rtxPro6000ShortName); ok {
		t.Fatal("expected not-found when mig-profile-tables.yaml key is missing")
	}
}

func TestConfigMapGPUDataSource_MalformedYAML(t *testing.T) {
	cm := testConfigMap("kube-system", map[string]string{
		fullGPUAttributesKey: "not: valid: yaml: [",
		migProfileTablesKey:  "not: valid: yaml: [",
	})
	src := newConfigMapGPUDataSource(newTestConfigMapLister(t, "kube-system", cm), nil)

	if _, ok := src.fullGPUAttributes("A10G"); ok {
		t.Fatal("expected not-found for malformed YAML")
	}
	if _, ok := src.migVariants(rtxPro6000ShortName); ok {
		t.Fatal("expected not-found for malformed YAML")
	}
}

func TestConfigMapGPUDataSource_ShortNameMiss(t *testing.T) {
	cm := testConfigMap("kube-system", map[string]string{
		fullGPUAttributesKey: "A10G:\n  productName: NVIDIA A10G\n",
		migProfileTablesKey:  "A100: []\n",
	})
	src := newConfigMapGPUDataSource(newTestConfigMapLister(t, "kube-system", cm), nil)

	if _, ok := src.fullGPUAttributes("H100"); ok {
		t.Fatal("expected not-found for a shortname absent from the parsed data")
	}
	if _, ok := src.migVariants("H100"); ok {
		t.Fatal("expected not-found for a shortname absent from the parsed data")
	}
}

// TestConfigMapGPUDataSource_InvalidQuantity checks that a variant with an unparseable or
// non-positive quantity is dropped rather than reaching aws_dra_mig.go's resource.MustParse,
// which would panic on a bad string; a sibling valid variant for the same short name must
// still be returned.
func TestConfigMapGPUDataSource_InvalidQuantity(t *testing.T) {
	migYAML := `
A100:
  - gpuMemoryMiB: 40960
    productName: "bad whole memory"
    brand: "Nvidia"
    architecture: "Ampere"
    memorySlices: 1
    whole: {memory: "not-a-quantity"}
    profiles: []
  - gpuMemoryMiB: 0
    productName: "non-positive gpuMemoryMiB"
    brand: "Nvidia"
    architecture: "Ampere"
    memorySlices: 1
    whole: {memory: "40Gi"}
    profiles: []
  - gpuMemoryMiB: 80000
    productName: "bad profile engines.memory"
    brand: "Nvidia"
    architecture: "Ampere"
    memorySlices: 1
    whole: {memory: "80Gi"}
    profiles:
      - name: "1g.10gb"
        profileID: 0
        memorySlices: 1
        placements: [0]
        engines: {memory: "also-not-a-quantity"}
  - gpuMemoryMiB: 40960
    productName: "the only valid variant"
    brand: "Nvidia"
    architecture: "Ampere"
    memorySlices: 1
    whole: {memory: "40Gi"}
    profiles: []
`
	cm := testConfigMap("kube-system", map[string]string{migProfileTablesKey: migYAML})
	src := newConfigMapGPUDataSource(newTestConfigMapLister(t, "kube-system", cm), nil)

	got, ok := src.migVariants("A100")
	if !ok {
		t.Fatal("expected the one valid variant to still be returned")
	}
	if len(got) != 1 {
		t.Fatalf("got %d variants, want 1 (the three invalid ones should be dropped): %+v", len(got), got)
	}
	if got[0].ProductName != "the only valid variant" {
		t.Fatalf("got variant %+v, want the valid one to survive", got[0])
	}
}

// TestConfigMapGPUDataSource_AllVariantsInvalid checks the not-found fallback when every
// variant for a short name fails validation, rather than returning an empty-but-ok slice.
func TestConfigMapGPUDataSource_AllVariantsInvalid(t *testing.T) {
	migYAML := `
A100:
  - gpuMemoryMiB: 40960
    productName: "bad whole memory"
    brand: "Nvidia"
    architecture: "Ampere"
    memorySlices: 1
    whole: {memory: "not-a-quantity"}
    profiles: []
`
	cm := testConfigMap("kube-system", map[string]string{migProfileTablesKey: migYAML})
	src := newConfigMapGPUDataSource(newTestConfigMapLister(t, "kube-system", cm), nil)

	if _, ok := src.migVariants("A100"); ok {
		t.Fatal("expected not-found when every variant for the short name is invalid")
	}
}

// TestConfigMapGPUDataSource_HappyPath round-trips a realistic RTX-6000-shaped YAML blob
// through the ConfigMap parser and checks it snapshot-equals the hardcoded fixture value —
// a regression guard that the YAML schema and the Go mapper stay consistent.
func TestConfigMapGPUDataSource_HappyPath(t *testing.T) {
	migYAML := `
RTX PRO Server 6000:
  - gpuMemoryMiB: 98304
    productName: "NVIDIA RTX PRO 6000 Blackwell Server Edition"
    brand: "Nvidia"
    architecture: "Blackwell"
    cudaComputeCapability: "12.0.0"
    memorySlices: 12
    whole: {multiprocessors: 188, copyEngines: 4, decoders: 4, encoders: 4, jpegEngines: 4, ofaEngines: 1, memory: "95Gi"}
    profiles:
      - name: "1g.24gb"
        profileID: 14
        memorySlices: 3
        placements: [0, 3, 6, 9]
        engines: {multiprocessors: 46, copyEngines: 1, decoders: 1, encoders: 1, jpegEngines: 1, ofaEngines: 0, memory: "24192Mi"}
      - name: "2g.48gb"
        profileID: 5
        memorySlices: 6
        placements: [0, 6]
        engines: {multiprocessors: 94, copyEngines: 2, decoders: 2, encoders: 2, jpegEngines: 2, ofaEngines: 0, memory: "48512Mi"}
      - name: "4g.96gb"
        profileID: 0
        memorySlices: 12
        placements: [0]
        engines: {multiprocessors: 188, copyEngines: 4, decoders: 4, encoders: 4, jpegEngines: 4, ofaEngines: 1, memory: "95Gi"}
`
	// Sanity-check the YAML itself parses into the expected internal type before comparing.
	var tables map[string][]migVariant
	if err := yaml.Unmarshal([]byte(migYAML), &tables); err != nil {
		t.Fatalf("failed to parse fixture YAML: %v", err)
	}

	cm := testConfigMap("kube-system", map[string]string{migProfileTablesKey: migYAML})
	src := newConfigMapGPUDataSource(newTestConfigMapLister(t, "kube-system", cm), nil)

	got, ok := src.migVariants(rtxPro6000ShortName)
	if !ok {
		t.Fatal("expected the RTX PRO Server 6000 entry to be found")
	}
	want := testGPUDataSource().migs[rtxPro6000ShortName]
	if len(got) != len(want) {
		t.Fatalf("got %d variants, want %d", len(got), len(want))
	}
	if got[0].ProductName != want[0].ProductName || got[0].MemorySlices != want[0].MemorySlices {
		t.Fatalf("parsed variant doesn't match fixture: got %+v, want %+v", got[0], want[0])
	}
	if len(got[0].Profiles) != len(want[0].Profiles) {
		t.Fatalf("got %d profiles, want %d", len(got[0].Profiles), len(want[0].Profiles))
	}
}
