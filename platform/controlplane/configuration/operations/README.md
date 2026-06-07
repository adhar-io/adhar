# Operations (Crossplane v2 day-2)

Crossplane v2 adds the **Operations API** (`ops.crossplane.io/v1alpha1`) for
day-2 tasks that don't fit the "reconcile a desired-state XR" model — backups,
secret rotation, drift detection/remediation. Operation pipelines reuse the same
composition functions; here they run [`function-python`](../functions/functions.yaml)
(input `python.fn.crossplane.io/v1beta1` `Script`, `operate()` entrypoint).

| Kind | Trigger | What it does |
|---|---|---|
| `Operation` | runs once | not used here |
| `CronOperation` | cron schedule | scheduled tasks (backup, rotation) |
| `WatchOperation` | resource create/update/delete | reactive tasks (drift) |

## Files

### `backup-cronoperation.yaml` — `adhar-daily-backup`
A `CronOperation` on `schedule: "0 2 * * *"` (daily, 02:00). Its pipeline emits a
**Velero `Backup` (`velero.io/v1`)** for the `adhar-system` and
`crossplane-system` namespaces (168h TTL). `concurrencyPolicy: Forbid` prevents
overlapping runs; `successfulHistoryLimit: 5` / `failedHistoryLimit: 3` cap the
retained `Operation` objects.

### `secret-rotation-cronoperation.yaml` — `adhar-weekly-secret-rotation`
A `CronOperation` on `schedule: "0 3 * * 0"` (weekly, Sunday 03:00). It stamps
the external-secrets `force-sync` annotation onto the `adhar-platform-secrets`
`ExternalSecret` in `adhar-system`, forcing external-secrets to re-pull from its
backing store — a rotation/refresh demonstration that leaves the desired spec
untouched.

### `drift-watchoperation.yaml` — `adhar-configmap-drift`
A `WatchOperation` watching `ConfigMap`s in `adhar-system` labeled
`adhar.io/managed=true`. Each matching change triggers a one-shot `Operation`
that reads the watched resource (injected as the required resource
`ops.crossplane.io/watched-resource`) and records its name, `resourceVersion`,
and data keys via `rsp.output` — a minimal read-only drift recorder.

## How resources flow

Inside an `operate(req, rsp)` function:

- **Apply a resource**: write it into `rsp.desired.resources["<key>"].resource`
  (full GVK + `metadata`). Crossplane server-side **force-applies** it (no owner
  references). Used by the backup and rotation operations.
- **Report results**: write to `rsp.output[...]` (or `response.set_output`).
  Surfaced on the `Operation` status for history/observability.
- **Read the trigger** (WatchOperation only):
  `request.get_required_resource(req, "ops.crossplane.io/watched-resource")`.

## Requirements

These need the core **`--enable-operations`** alpha flag, which Adhar sets in
[`hack/crossplane/values.yaml`](../../../../hack/crossplane/values.yaml) and
[`hack/crossplane/values-ha.yaml`](../../../../hack/crossplane/values-ha.yaml)
(`args: [--enable-operations]`). `function-python` must be installed (it is, via
`configuration/functions/functions.yaml`). The backup operation additionally
requires Velero CRDs + a configured `BackupStorageLocation`; the rotation
operation requires the external-secrets CRDs.

## Applying

The platform controller applies this directory as part of the control plane.
To apply manually:

```bash
kubectl apply -f platform/controlplane/configuration/operations/

# Inspect
kubectl get cronoperations,watchoperations
kubectl get operations            # one per CronOperation run / WatchOperation trigger
kubectl describe operation <name> # see pipeline output

# Trigger a CronOperation immediately for testing (creates an Operation from its template)
kubectl get cronoperation adhar-daily-backup -o yaml
```
