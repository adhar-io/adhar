# Kyverno half of preview environments

Applied by the `preview-environments-kyverno` ApplicationSet element, separate
from `preview-environments` so previews work without Kyverno (the local
curated core turns Kyverno off). Requires the `kyverno` package:

- `10-preview-guardrails.yaml` — the ClusterPolicy that fences `preview-*`
  namespaces (quotas, no cluster-scoped escapes).
