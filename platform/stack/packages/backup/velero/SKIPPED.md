# backup/velero — SKIPPED (duplicate)

This package is intentionally left empty and is **not implemented**.

It is a **duplicate of `core/velero`**, which is the real, deployed Velero
package (rendered manifests at
`platform/stack/packages/core/velero/manifests/install.yaml`, wired into the
ArgoCD ApplicationSet `platform/stack/adhar-appset-local.yaml` as the `velero`
application).

To avoid two conflicting Velero installations (CRDs, the `velero` Deployment,
BackupStorageLocation, etc.), no second Velero is rendered here. This stub is
retained only as a placeholder. Use `core/velero` for backups.
