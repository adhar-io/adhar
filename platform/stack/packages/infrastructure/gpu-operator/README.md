# gpu-operator

The [NVIDIA GPU Operator](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/)
makes a node's GPUs schedulable: driver, container toolkit, device plugin,
DCGM metrics and GPU feature discovery, all as operator-managed DaemonSets on
whichever nodes NFD finds a GPU on. Pinned to chart **v26.7.1**.

It advertises the `nvidia.com/gpu` that `ai/llm-d`'s GPU profile
(Qwen3.6-27B-FP8) and every ApplicationType `ai` workload schedule against.

## Two kinds of GPU node

The operator treats a node by its `nvidia.com/gpu.workload.config` label:

| Label value | What runs there | Who lands there |
|---|---|---|
| `container` (default for unlabelled nodes) | driver, toolkit, device plugin, DCGM, GFD | `ai/llm-d` GPU profile; AI applications with `isolation: container`; anything else that requests `nvidia.com/gpu` |
| `vm-passthrough` | vfio-manager (GPUs bound to VFIO) and the Kata sandbox device plugin | AI applications with `isolation: microvm` that request a GPU, via `infrastructure/kata-containers`' `kata-qemu-nvidia-gpu` RuntimeClass |

```bash
kubectl label node <name> nvidia.com/gpu.workload.config=vm-passthrough   # also: adhar.io/microvm=true
```

NVIDIA's Kata Manager is left **off**: the Kata runtime comes from
`infrastructure/kata-containers`, and two installers fighting over the
`kata-qemu-nvidia-gpu` RuntimeClass is worse than one.

## GPU sharing on container nodes

`values.yaml` ships a device-plugin ConfigMap (`adhar-gpu-sharing`) with three
profiles. Pick one per node:

```bash
kubectl label node <name> nvidia.com/device-plugin.config=mig-single
```

| Profile | What it does | GFD label the isolation policy selects on |
|---|---|---|
| `dedicated` (default) | whole GPUs, no sharing | — |
| `mig-single` | every GPU partitioned into same-sized MIG slices; **each slice is one `nvidia.com/gpu`**, hardware-isolated | `nvidia.com/mig.strategy=single` |
| `time-slice-4` | each GPU advertised 4×, shared round-robin, **no memory isolation** between tenants | `nvidia.com/gpu.sharing-strategy=time-slicing` |

MIG also needs the partition applied: label the node
`nvidia.com/mig.config=<profile>` (`all-1g.10gb`, `all-3g.40gb`, … — see
`nvidia-smi mig -lgip` for what the card supports); the MIG manager does the
rest. The default partition is `all-disabled`.

A `Application` with `parameters.ai.gpu.sharing: mig` is placed on a
`mig.strategy=single` node by the `ai-workload-isolation` policy, so its
`ai.gpu.count: 2` is two slices with no change to how it requests them;
`timeslice` lands on a time-slicing node; `none` on any GPU node. The labels
are published by GPU feature discovery from the node's **live** device-plugin
configuration, so a pod can only land where the sharing it asked for is
actually in effect.

## GPU passthrough and the resource name

On a `vm-passthrough` node the sandbox device plugin advertises each GPU under
its **product** name (`nvidia.com/GA102GL_A10`, …), not `nvidia.com/gpu`.
`manifests/passthrough.yaml` ships the `adhar-gpu-passthrough` ConfigMap,
empty; fill in `resource` once per cluster:

```bash
kubectl get node -l nvidia.com/gpu.workload.config=vm-passthrough -o jsonpath='{.items[0].status.allocatable}'
```

and the isolation policy rewrites `nvidia.com/gpu` in microVM AI pods to it.
While it is empty those pods stay Pending — visible, never silently
un-isolated. Passthrough is one whole GPU per pod; MIG and time-slicing do not
apply behind VFIO.

## Cloud images with a driver already installed

GKE COS, the EKS GPU AMIs and similar ship the driver: set `driver.enabled:
false` (and `toolkit.enabled: false` where the toolkit is pre-installed) in
`values.yaml` for that cluster.

## What ships

| File | What it is |
|---|---|
| `manifests/install.yaml` | the operator, NFD, CRDs (wave -5), the `ClusterPolicy` CR (wave 5) and the DCGM ServiceMonitor (wave 10), rendered from the chart |
| `manifests/passthrough.yaml` | the `adhar-gpu-passthrough` ConfigMap |
| `values.yaml` | sandbox mode `kata`, the three sharing profiles, MIG manager, DCGM ServiceMonitor |

The chart's three `helm.sh/hook` Jobs (CRD upgrade, GPU-cluster cleanup, NFD
prune) are dropped by the generator — there is no Helm release under Argo CD.

## Regenerating

```bash
./generate-manifests.sh   # bump CHART_VERSION here and version/appVersion in adhar-package.yaml together
```
