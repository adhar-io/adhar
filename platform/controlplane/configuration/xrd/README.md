# Composite Resource Definitions

Define one XRD per command (or command group) to expose a declarative API that mirrors the `adhar` CLI.  XRD files should:

- Use the `platform.adhar.io` API group
- Default to version `v1alpha1` until stabilised
- Expose fields that align with existing Go types under `platform/types`
- Reference compositions via label selectors such as `feature` and `provider`

Example file naming convention: `cluster.xrd.yaml`, `apps.xrd.yaml`, etc.
