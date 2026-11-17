#!/usr/bin/env python3
"""Remove cluster-scoped write access from External Secrets' aggregated `edit` role.

The upstream chart's `external-secrets-edit` ClusterRole is aggregated into the
built-in `edit` and `admin` roles and lets them create, patch and delete
ClusterSecretStores, ClusterPushSecrets and ClusterGenerators. Through a
namespace RoleBinding that is harmless (cluster-scoped grants do not apply), but
Adhar binds the `platform-developer` OIDC group to `edit` cluster-wide
(keycloak/manifests/k8s-rbac.yaml). Any platform developer could then create an
unrestricted ClusterSecretStore and bypass the namespace conditions the
platform's own stores carry — reading any secret the backend can reach.

Namespaced self-service (ExternalSecret, SecretStore, PushSecret and the
namespaced generators) is untouched. Platform stores are managed by GitOps, not
by people holding `edit`.

Run by generate-manifests.sh after `helm template`; idempotent. Fails loudly if
the chart's role no longer has the expected shape, so a regeneration can never
silently reopen the gap.
"""
import re
import sys

CLUSTER_SCOPED = ("clustersecretstores", "clusterpushsecrets", "clustergenerators")
MARKER = "# adhar: cluster-scoped resources removed by patch-rbac.py"


def patch(text: str) -> str:
    docs = text.split("\n---\n")
    found = False
    for i, doc in enumerate(docs):
        if not re.search(r"^kind: ClusterRole$", doc, re.M) or not re.search(
            r"^  name: external-secrets-edit$", doc, re.M
        ):
            continue
        found = True
        if MARKER in doc:
            return text  # already patched
        lines = doc.split("\n")
        kept = [
            line
            for line in lines
            if line.strip().strip("-").strip().strip('"') not in CLUSTER_SCOPED
        ]
        removed = len(lines) - len(kept)
        if removed != len(CLUSTER_SCOPED):
            sys.exit(
                f"patch-rbac.py: expected to remove {len(CLUSTER_SCOPED)} cluster-scoped "
                f"resources from external-secrets-edit, removed {removed}; review the chart's RBAC"
            )
        out = []
        for line in kept:
            out.append(line)
            if line == "rules:":
                out.append(
                    "  " + MARKER + " (cluster-wide `edit` must not create ClusterSecretStores)"
                )
        docs[i] = "\n".join(out)
    if not found:
        sys.exit("patch-rbac.py: ClusterRole external-secrets-edit not found; review the chart's RBAC")
    return "\n---\n".join(docs)


if __name__ == "__main__":
    path = sys.argv[1]
    with open(path) as f:
        original = f.read()
    patched = patch(original)
    if patched != original:
        with open(path, "w") as f:
            f.write(patched)
        print(f"patch-rbac.py: removed {', '.join(CLUSTER_SCOPED)} write access from external-secrets-edit")
    else:
        print("patch-rbac.py: already patched")
