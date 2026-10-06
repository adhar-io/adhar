# ai-workload-isolation — CLI check

The policy is a set of mutations keyed on NAMESPACE labels, which the Kyverno
CLI cannot see from the pod alone; `values.yaml` supplies the labels of four
namespaces (microVM+MIG, container+MIG, container+time-slice, and a plain
`service` namespace that must be left alone) and the passthrough resource name
the `adhar-gpu-passthrough` ConfigMap would carry.

```bash
cd platform/stack/packages/security/adhar-kyverno-policies
kyverno apply manifests/ai-workload-isolation.yaml \
  --resource tests/ai-workload-isolation/resources.yaml \
  -f tests/ai-workload-isolation/values.yaml -o /tmp/ai-isolation
```

Expected, per pod (`/tmp/ai-isolation/*.yaml`):

| Pod | RuntimeClass | nodeSelector | GPU resource |
|---|---|---|---|
| `vision-prod/gpu-microvm` | `kata-qemu-nvidia-gpu` | `nvidia.com/gpu.workload.config=vm-passthrough` | renamed to `nvidia.com/GA102GL_A10` in limits AND requests |
| `vision-prod/cpu-microvm` | `kata-qemu` | — | — |
| `vision-prod/explicit-runc` | `runc` (untouched: it chose) | — | — |
| `embed-prod/gpu-mig` | — | `gpu.present=true`, `mig.strategy=single`; its own toleration kept, `nvidia.com/gpu` added | unchanged |
| `slice-prod/gpu-slice` | — | `gpu.present=true`, `gpu.sharing-strategy=time-slicing` | unchanged |
| `team-web/plain` | — (not an `ai` namespace: nothing applies) | — | unchanged |

Every mutated pod is annotated `platform.adhar.io/isolation-effective` and,
for GPU pods, `platform.adhar.io/gpu-sharing-effective` — `dedicated` on a
microVM, whatever the namespace asked for, because a passthrough GPU cannot
be shared.
