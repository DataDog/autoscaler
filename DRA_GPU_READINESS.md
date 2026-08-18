# GPU Node Readiness: Device Plugin vs. DRA

How Cluster Autoscaler decides whether a GPU node is "ready", why there are two independent
mechanisms for it, and where the DRA path falls short.

**Analyzed at:** branch **`datadog-master-18.0`**, commit
`f6ae2e565ae9e1dc103ac8ba7f7aacd8752aa0ae` — the most up-to-date branch, chosen deliberately because
essentially all of the DRA machinery described here landed in 18.0. Every file path, line number, and
code excerpt below was read from that tree.

---

# 1. Context

## 1.1 Why GPU nodes need special readiness handling

A node can register with the kubelet and report `Ready` *before* its GPU is actually usable, because
driver / device-plugin installation happens asynchronously after the node joins
(see [kubernetes#54959](https://github.com/kubernetes/kubernetes/issues/54959)).

If CA trusted `Ready` at face value it would believe a scale-up had succeeded before the GPU became
schedulable, and would stop adding capacity while GPU pods were still unschedulable. So CA overrides
the status of such nodes to unready and lets its normal "node still booting" logic handle them.

## 1.2 Two ways a GPU reaches Kubernetes

The catch is that GPUs can be surfaced in two entirely different ways, and readiness has to be
detected differently for each:

| | Classic (device plugin) | DRA (Dynamic Resource Allocation) |
|---|---|---|
| How the GPU appears | Extended resource in `node.Status.Allocatable`, e.g. `nvidia.com/gpu` | `ResourceSlice` objects published by a DRA driver |
| Readiness signal | Allocatable count becomes non-zero | Driver finishes publishing complete resource pools |
| Processor | `GpuCustomResourcesProcessor` | `DraCustomResourcesProcessor` |
| Providers wired up | All | **GCE only** (see §2.4) |

The essential asymmetry: **a DRA-exposed GPU never appears in `Status.Allocatable`**, so the classic
readiness check can never be satisfied by one. That single fact is the root of both issues in §3.

## 1.3 ⚠️ This document does not apply to `datadog-master-17.0`

`datadog-master-17.0` (`6a50d0ee39`) predates the entire DRA-GPU integration. None of the following
exist there:

| Symbol / file | 17.0 (`6a50d0ee39`) | 18.0 (`f6ae2e565a`) |
|---|---|---|
| `GpuConfig.DraDriverName` / `ExposedViaDra()` | ❌ absent | ✅ `cloudprovider/cloud_provider.go:100-111` |
| `cloudprovider/gce/dynamicresources.go` | ❌ file does not exist | ✅ `GpuDraDriverEnabled` + DRA GPU label |
| DRA opt-out inside the GPU processor | ❌ absent | ✅ `gpu_processor.go:46` |
| DRA pool comparison | `isEqualResourceSlices` — exact **set equality** of device names | `areResourcePoolsReady` — **complete-pool count per driver** |
| `TemplateNodeInfoRegistry` | ❌ absent | ✅ per-loop template cache, preferred by the DRA processor |
| `ReportResourceDiscrepancies` drift metrics | ❌ absent | ✅ `dra_processor.go:98` |
| `CSICustomResourcesProcessor` | ❌ absent | ✅ third processor in the chain |
| `--enable-dynamic-resource-allocation` | default **`false`** | default **`true`**, locked (CA fatals if false) |

Note the last row: on 17.0 DRA handling was off by default; on 18.0 it is always on
(`config/flags/flags.go:237` and `flags.go:310-312`).

---

# 2. How the mechanism works

## 2.1 Entry point and the processor chain

There is one call site, in the main autoscaling loop:

```
core/static_autoscaler.go:1232
  allNodes, readyNodes = a.processors.CustomResourcesProcessor.FilterOutNodesWithUnreadyResources(
      a.AutoscalingContext, allNodes, readyNodes, draSnapshot, csiSnapshot)
```

Nodes deemed unready are dropped from `readyNodes` and replaced in `allNodes` with
`kubernetes.GetUnreadyNodeCopy(node, kubernetes.ResourceUnready)`.

Behind that interface sits a chain assembled by `NewDefaultCustomResourcesProcessor`
(`processors/customresources/default_custom_processor.go:35-44`), wired from
`processors/processors.go:100`:

```go
func NewDefaultCustomResourcesProcessor(draEnabled bool, csiEnabled bool) CustomResourcesProcessor {
	customProcessors := []CustomResourcesProcessor{&GpuCustomResourcesProcessor{}}
	if draEnabled {
		customProcessors = append(customProcessors, NewDraCustomResourcesProcessor())
	}
	if csiEnabled {
		customProcessors = append(customProcessors, &CSICustomResourcesProcessor{})
	}
	return &DefaultCustomResourcesProcessor{customProcessors}
}
```

The GPU processor is unconditional; the DRA one is gated on
`--enable-dynamic-resource-allocation`, which defaults to `true` and is locked (CA calls
`klog.Fatalf` if set to `false`), so in practice both are always present. The CSI processor is
unrelated to GPUs.

## 2.2 ⚠️ Chain semantics: subtractive and ordered

This is the single most important detail for understanding §3.

```go
// default_custom_processor.go:47-54
newAllNodes := allNodes
newReadyNodes := readyNodes
for _, processor := range p.customResourcesProcessors {
	newAllNodes, newReadyNodes = processor.FilterOutNodesWithUnreadyResources(
		autoscalingCtx, newAllNodes, newReadyNodes, draSnapshot, csiSnapshot)
}
```

Each processor's **output** is the next one's input. Therefore:

1. **The GPU processor always runs first.**
2. **A processor can only remove nodes from `readyNodes`, never add them back.** Once the GPU
   processor marks a node unready, the DRA processor never sees it in its input and cannot correct
   the decision. The verdict is irreversible within a loop iteration.

## 2.3 The classic GPU processor

`processors/customresources/gpu_processor.go:41-70`:

```go
for _, node := range readyNodes {
    if gpuExposedViaDra(autoscalingCtx, node) {
        newReadyNodes = append(newReadyNodes, node)
        continue                                    // <-- opt out, defer to DRA processor
    }

    _, hasGpuLabel := node.Labels[autoscalingCtx.CloudProvider.GPULabel()]
    _, hasAnyGpuAllocatable := gpu.NodeHasGpuAllocatable(node)
    if hasGpuLabel && !hasAnyGpuAllocatable {
        // has the label, but no allocatable GPU -> assume drivers still installing
        nodesWithUnreadyGpu[node.Name] = kubernetes.GetUnreadyNodeCopy(node, kubernetes.ResourceUnready)
    } else {
        newReadyNodes = append(newReadyNodes, node)
    }
}
```

The rule is: **"label says GPU, but no allocatable GPU ⇒ not ready."**

- `utils/gpu/gpu.go:138` — `NodeHasGpuAllocatable` scans **all** known vendor resource names
  (`GPUVendorResourceNames`, `gpu.go:46`: `nvidia.com/gpu`, `microsoft.com/directx`, others), not
  just Nvidia.
- The GPU label is provider-specific: `k8s.amazonaws.com/accelerator` on AWS
  (`cloudprovider/aws/aws_cloud_provider.go:48`), a different label on GCE, etc.

## 2.4 The DRA opt-out (`ExposedViaDra`)

`gpu_processor.go:132-142` asks the cloud provider whether this node's GPU is DRA-exposed, and if so
abstains:

```go
func gpuExposedViaDra(autoscalingCtx *ca_context.AutoscalingContext, node *apiv1.Node) bool {
	gpuConfig := autoscalingCtx.CloudProvider.GetNodeGpuConfig(node)
	if gpuConfig == nil {
		return false
	}
	// Devices attached through DRA are not using node allocatable
	// to confirm their attachment, assume that node is ready
	// and will be checked in the separate processor
	return gpuConfig.ExposedViaDra()
}
```

The signal lives on `GpuConfig` (`cloudprovider/cloud_provider.go:100-111`):

```go
type GpuConfig struct {
	Label                string
	Type                 string
	ExtendedResourceName apiv1.ResourceName
	DraDriverName        string
}

func (gpu *GpuConfig) ExposedViaDra() bool {
	return gpu.DraDriverName != ""
}
```

### Only GCE populates `DraDriverName`

`cloudprovider/gce/gce_cloud_provider.go:96-109`:

```go
func (gce *GceCloudProvider) GetNodeGpuConfig(node *apiv1.Node) *cloudprovider.GpuConfig {
	gpuConfig := gpu.GetNodeGPUFromCloudProvider(gce, node)

	// If GPU devices are exposed using DRA - extended resource
	// won't be present in the node alloctable or capacity
	// so we overwrite extended resource name as it won't ever be there
	if GpuDraDriverEnabled(node) {
		gpuConfig.DraDriverName = DraGPUDriver
		gpuConfig.ExtendedResourceName = ""
	}
	return gpuConfig
}
```

Detection is a single node label (`cloudprovider/gce/dynamicresources.go`):

```go
const (
	DraGPUDriver = "gpu.nvidia.com"
	DraGPULabel  = "cloud.google.com/gke-gpu-dra-driver"
)

func GpuDraDriverEnabled(node *apiv1.Node) bool {
	return node.Labels[DraGPULabel] == "true"
}
```

Every other provider goes through the shared `gpu.GetNodeGPUFromCloudProvider`
(`utils/gpu/gpu.go:174`), which **never sets `DraDriverName`**. There is no AWS/Azure equivalent of
`gce/dynamicresources.go`. This is the direct cause of Issue 1 (§3.1).

`ExposedViaDra()` also gates GPU accounting elsewhere:

- `simulator/utilization/info.go:51` — DRA nodes skip extended-resource GPU utilization and fall
  through to `HighestDynamicResourceUtilization` (itself marked provisional with a `TODO(DRA)`).
- `utils/gpu/gpu.go:86-89` — metrics report a synthetic `dra_<driver>` resource name, since there is
  no capacity-backed name to report.

## 2.5 The DRA processor

`processors/customresources/dra_processor.go:55-109`. For each node still in `readyNodes`:

1. Resolve the node group (`NodeGroupForNode`).
2. Get the template `NodeInfo` via `getNodeInfo` (`dra_processor.go:111-118`), which **prefers the
   cached template from `TemplateNodeInfoRegistry`** over a direct `ng.TemplateNodeInfo()` call — the
   cached one may carry DRA slices the raw cloud-provider template lacks. This preference is
   load-bearing; see §2.6.
3. Fetch the node's live slices: `draSnapshot.NodeResourceSlices(node.Name)`.
4. Compare with `areResourcePoolsReady(nodeSlices, templateNodeInfo.LocalResourceSlices)`.
5. Report template-vs-actual drift metrics for the nodes that **passed**, via
   `ReportResourceDiscrepancies` (`dra_processor.go:98`).

### The comparison: complete pools per driver

`areResourcePoolsReady` (`dra_processor.go:143-154`) is a **minimum** check, not an equality check:

```go
for driver, count := range templatePools {
    if realPools[driver] < count {
        return false
    }
}
return true
```

Extra devices or pools beyond what the template declares are fine; only a *shortfall* is unready.

`getCompleteResourcePools` (`dra_processor.go:157-190`) defines "complete", and this is what actually
detects a driver that is mid-publish:

- Group slices by `(Pool.Name, Spec.Driver)`.
- Keep only the highest `Pool.Generation` seen per pool; discard older generations.
- A pool counts as ready only when the number of observed slices **exactly equals**
  `Pool.ResourceSliceCount` — the count the driver itself declares.

So a partially published pool (say 3 of 8 declared slices written so far) does not count, and the
node stays unready until the driver finishes.

### Fail-open behaviour

The DRA processor keeps a node in `readyNodes` on every error path:

| Condition | Result |
|---|---|
| `draSnapshot == nil` | logs a warning, returns inputs **unchanged** (whole processor skipped) |
| `NodeGroupForNode` returns an error | node kept ready, warning logged |
| node group is `nil` (unmanaged node) | node kept ready |
| template `NodeInfo` unavailable | node kept ready, warning logged |

This is a deliberate bias toward not falsely marking nodes unready, but it means DRA readiness
enforcement quietly disappears whenever templates or node groups cannot be resolved.

## 2.6 Where templates get their ResourceSlices

Since the DRA check is "live slices vs. template slices", the template's contents decide whether it
verifies anything at all. `MixedTemplateNodeInfoProvider`
(`processors/nodeinfosprovider/mixed_nodeinfos_processor.go:118-156`) tries three sources in order:

1. **Copied from a real running node in the group** (preferred).
   `SanitizedTemplateNodeInfoFromNodeInfo` carries `sanitizedExample.LocalResourceSlices` into the
   template (`simulator/node_info_utils.go:77`), after `createSanitizedNodeInfo` rewrites them for
   the synthetic node name via `drautils.SanitizedNodeResourceSlices` (`node_info_utils.go:101`) —
   pool names suffixed, node names rewritten. ✅ **Has slices.**
2. **The node-info cache** — a `DeepCopy` of a template previously built by path 1.
   ✅ **Has slices.**
3. **`nodeGroup.TemplateNodeInfo()`** — the scale-from-zero fallback, used only when the group has no
   usable running node. Whether it has slices is entirely up to the cloud provider:
   - ❌ **`nil` for most providers**, including **GCE and AWS** — plus alicloud, azure, hetzner,
     civo, ovhcloud, digitalocean, equinixmetal, volcengine, utho, kwok, oci.
   - ✅ Only `clusterapi` (`cloudprovider/clusterapi/clusterapi_nodegroup.go:385`) and `coreweave`
     actually build `resourceSlices`.

The consequence of path 3 returning `nil` is Issue 2 (§3.2).

## 2.7 Decision flow

```
FilterOutNodesWithUnreadyResources(allNodes, readyNodes, draSnapshot, csiSnapshot)
│
├─ 1. GpuCustomResourcesProcessor  (ALWAYS runs, ALWAYS first)
│     ├─ GetNodeGpuConfig(node).ExposedViaDra()?     (GCE-only signal today)
│     │    └─ true  -> keep ready, defer to DRA processor
│     └─ false -> hasGpuLabel && !hasAnyGpuAllocatable ?
│                   └─ yes -> MARK UNREADY (irreversible)   [Issue 1 on non-GCE DRA]
│
├─ 2. DraCustomResourcesProcessor  (--enable-dynamic-resource-allocation, locked true)
│     └─ compare live ResourceSlices vs template LocalResourceSlices
│          ├─ template has no slices -> passes vacuously     [Issue 2]
│          └─ complete pools per driver < template -> MARK UNREADY
│
└─ 3. CSICustomResourcesProcessor  (only if CSI-node-aware scheduling; unrelated to GPUs)
```

---

# 3. Issues

## 3.1 Issue 1: non-GCE DRA GPU nodes can be stuck unready forever

**Condition:** not GCE **and** the node carries the cloud provider's GPU label **and** its GPUs are
exposed only via DRA (no `nvidia.com/gpu`-style allocatable).

**What happens:**

1. `GetNodeGpuConfig` never sets `DraDriverName` outside GCE (§2.4) ⇒ `ExposedViaDra()` is `false`.
2. The opt-out at `gpu_processor.go:46` does not fire.
3. The classic rule applies: `hasGpuLabel && !hasAnyGpuAllocatable` is `true` ⇒ node marked unready.
4. Because DRA drivers never populate extended-resource allocatable, this condition **never
   resolves**. The node is permanently unready.
5. The DRA processor cannot rescue it — the GPU processor already removed it from `readyNodes`
   (§2.2).

**This is not unconditional.** It hinges entirely on whether the provider's GPU label is present:

| GPU label present? | DRA-only GPUs | Outcome |
|---|---|---|
| Yes | Yes | ❌ **Permanently unready** |
| No | Yes | ✅ GPU processor is a no-op; DRA processor performs the real check |
| Yes | No (classic device plugin) | ✅ Normal behaviour — unready until drivers install |

So a DRA rollout on AWS/Azure is safe *only* as long as the accelerator label is absent from those
nodes — a fragile, implicit dependency that nothing in the code enforces or documents.

**Fix direction:** populate `DraDriverName` in the other providers' `GetNodeGpuConfig`, mirroring
`gce/dynamicresources.go`. `ExposedViaDra()` is the only signal the GPU processor honors.

## 3.2 Issue 2: the DRA check is vacuous when the template has no slices

`areResourcePoolsReady` iterates over `templatePools`. **If the template has no ResourceSlices,
`templatePools` is empty, the loop body never executes, and the function returns `true` for every
node** — the readiness check silently passes without verifying anything.

Combined with the template sources in §2.6, DRA readiness is therefore **self-bootstrapping**:

- The **first** node of a brand-new GPU node group on GCE/AWS has no slice-bearing template
  (path 3 returns `nil`), so its DRA readiness is **not verified**.
- Once one node publishes slices and becomes a template candidate, **subsequent** nodes are checked
  against it (paths 1 and 2).

This is also why `getNodeInfo` prefers the registry over `ng.TemplateNodeInfo()`: the direct call
would return nil slices on GCE/AWS and make the check vacuous even for established groups. And it is
why `ReportResourceDiscrepancies` exists — template drift is expected, so it is surfaced as metrics
rather than treated as unreadiness.

**Fix direction:** have the provider's `TemplateNodeInfo()` publish the expected `ResourceSlices`,
as `clusterapi` and `coreweave` already do.

## 3.3 Lesser issue: fail-open hides loss of enforcement

Per the table in §2.5, every error path in the DRA processor keeps the node ready. A misconfigured
node group, a template build failure, or a nil DRA snapshot silently disables DRA readiness checking
— with only a `klog.Warningf` to show for it. Worth alerting on those warnings if you depend on the
check.

## 3.4 Practical checklist

- [ ] **Non-GCE?** Verify your GPU nodes do **not** carry the provider's GPU label
      (`k8s.amazonaws.com/accelerator` on AWS), or they will be permanently unready (§3.1).
- [ ] **Scale-from-zero groups:** accept that the first node's DRA readiness is unverified on
      GCE/AWS, or teach your provider's `TemplateNodeInfo()` to publish expected `ResourceSlices`
      (§3.2).
- [ ] **Watch the discrepancy metrics** from `ReportResourceDiscrepancies` for template drift (§2.5).
- [ ] **Watch for the fail-open warnings** — they mean the check silently stopped applying (§3.3).

---

# 4. References

## 4.1 Key files

| File | Role |
|---|---|
| `core/static_autoscaler.go:1232` | Single call site in the main loop |
| `processors/customresources/custom_resources_processor.go` | `CustomResourcesProcessor` interface |
| `processors/customresources/default_custom_processor.go` | Ordered chain, flag gating |
| `processors/customresources/gpu_processor.go` | Classic device-plugin readiness + DRA opt-out |
| `processors/customresources/dra_processor.go` | DRA ResourceSlice readiness |
| `cloudprovider/cloud_provider.go:100` | `GpuConfig` / `ExposedViaDra()` |
| `cloudprovider/gce/dynamicresources.go` | GCE DRA GPU label + driver name (only provider) |
| `cloudprovider/gce/gce_cloud_provider.go:96` | Populates `DraDriverName` |
| `utils/gpu/gpu.go` | `GPUVendorResourceNames`, `NodeHasGpuAllocatable`, `GetNodeGPUFromCloudProvider` |
| `config/flags/flags.go:237,310` | `--enable-dynamic-resource-allocation`, locked to `true` |
| `processors/nodeinfosprovider/mixed_nodeinfos_processor.go` | Template source priority |
| `processors/nodeinfosprovider/template_node_info_registry.go` | Per-loop template cache |
| `simulator/node_info_utils.go` | Slice sanitization into templates |
| `simulator/dynamicresources/snapshot/snapshot.go` | `NodeResourceSlices` lookup |
| `simulator/utilization/info.go:51` | DRA-aware utilization branch |

## 4.2 Outstanding TODOs in the code

Ones bearing on the mechanism above (verified present on `datadog-master-18.0`):

| Location | TODO |
|---|---|
| `core/static_autoscaler.go:1231` | `Remove this call when we handle dynamically provisioned resources.` — i.e. the whole readiness-override hack is meant to be temporary |
| `simulator/node_info_utils.go:46-48` | `TemplateNodeInfo()` returning DaemonSet pods that use DRA only works if those pods are already allocated — relevant to template contents (§2.6) |
| `simulator/node_info_utils.go:190` | Same problem for force-added DS pods |
| `simulator/utilization/info.go:63` | DRA node utilization is provisional — "should work well for Nodes with a single Pool of expensive Devices but is probably not flexible enough for other scenarios" |
| `simulator/dynamicresources/snapshot/snapshot_slice_lister.go:27` | `Actually handle the taint rules.` — device taints are ignored |
| `simulator/framework/infos.go:143` | ResourceClaim data returned may be stale |

## 4.3 External

- [kubernetes#54959](https://github.com/kubernetes/kubernetes/issues/54959) — nodes reporting `Ready`
  before GPUs are usable; the original motivation for this whole mechanism.
- [Kubernetes DRA documentation](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/)
- [kubernetes/autoscaler](https://github.com/kubernetes/autoscaler) — upstream, for comparing how far
  this fork has diverged.
