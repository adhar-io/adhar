# kata-containers

[Kata Containers](https://katacontainers.io) runs a pod inside its own
lightweight virtual machine — QEMU, a guest kernel, a guest agent — so a
container escape ends at a hypervisor boundary instead of at the node. On
this platform it is the isolation an **ApplicationType `ai`** workload gets
by default. Pinned to kata-deploy **3.21.0**.

## How it fits the platform

A `CompositeApplication` with `parameters.type: ai` composes its namespace
with `platform.adhar.io/isolation: microvm` (the default). The
`ai-workload-isolation` policy in `security/adhar-kyverno-policies` then
stamps every pod in that namespace with one of the RuntimeClasses this
package creates:

| RuntimeClass | Shim | When |
|---|---|---|
| `kata-qemu` | `qemu` | pods that request no GPU |
| `kata-qemu-nvidia-gpu` | `qemu-nvidia-gpu` | pods that request `nvidia.com/gpu` — the GPU is passed through to the VM with VFIO, which needs `infrastructure/gpu-operator` in sandbox mode `kata` and a node labelled `nvidia.com/gpu.workload.config=vm-passthrough` |

Nothing else selects them; the node's default runtime stays `runc`
(`createDefaultRuntimeClass: "false"`). Two shims are installed, not the
chart's thirteen: every extra shim is a guest kernel and rootfs on every node
and a RuntimeClass nobody here uses.

## Opt-in nodes

Kata needs hardware virtualisation (`/dev/kvm`) on the node. Kind, most cloud
VM shapes without nested virtualisation and every control-plane droplet do not
have it, so the installer DaemonSet only runs where you say:

```bash
kubectl label node <name> adhar.io/microvm=true
```

Once the runtime is installed kata-deploy labels the node
`katacontainers.io/kata-runtime=true` itself, and every RuntimeClass it creates
carries that label as its scheduling `nodeSelector` — a `kata-*` pod can only
ever land where the runtime is actually ready. A pod that stays Pending with
"no nodes match the RuntimeClass nodeSelector" is telling you no node has been
labelled.

A GPU microVM node carries both labels: `adhar.io/microvm=true` for this
package and `nvidia.com/gpu.workload.config=vm-passthrough` for the GPU
operator.

## What a microVM costs you

* **No GPU sharing.** A passed-through GPU is the VM's alone; MIG slices and
  time-slicing are container-runtime mechanisms and do not exist behind VFIO.
  `ai.gpu.sharing: mig|timeslice` with `isolation: microvm` gets a whole GPU,
  and the policy annotates the pod `platform.adhar.io/gpu-sharing-effective:
  dedicated` to say so. Choose `isolation: container` when sharing matters
  more than the VM boundary.
* **One GPU per pod** (NVIDIA's limit for Kata passthrough).
* **hostPath, host networking and privileged containers do not work** inside
  the VM; the guest has its own kernel. Ordinary volumes, Services and
  NetworkPolicies behave as before.
* A few hundred milliseconds of VM boot per pod start and ~150 MB of guest
  memory overhead.

## What ships

| File | What it is |
|---|---|
| `manifests/install.yaml` | ServiceAccount, RBAC and the installer DaemonSet, rendered from the chart |
| `values.yaml` | the two shims, no default RuntimeClass, the `adhar.io/microvm` node selector |

The chart's `helm.sh/hook: post-delete` cleanup Job is dropped by the
generator: there is no Helm release under Argo CD, and the DaemonSet's own
`preStop` (`kata-deploy reset`) already uninstalls the runtime when a pod is
removed from a node.

## Regenerating

```bash
./generate-manifests.sh   # bump CHART_VERSION here and version/appVersion in adhar-package.yaml together
```
