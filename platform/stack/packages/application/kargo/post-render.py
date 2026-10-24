#!/usr/bin/env python3
"""Edits the kargo chart cannot express, re-applied after every `helm template`
by generate-manifests.sh. Text-level and idempotent.

Kargo's own namespaces (kargo-cluster-secrets, kargo-shared-resources,
kargo-system-resources) carry no `adhar.io/plane`, so namespace-governance
reported each as a placement violation (2026-10-24). They hold Kargo's
controller state: the control plane.
"""
import re
import sys

NAMES = ("kargo-cluster-secrets", "kargo-shared-resources", "kargo-system-resources")


def main(path):
    s = open(path).read()
    for name in NAMES:
        m = re.search(r'kind: Namespace\nmetadata:\n  name: "?%s"?\n  labels:\n' % re.escape(name), s)
        if not m:
            sys.exit(f"post-render: Namespace {name} not found in {path}; the chart changed")
        block_end = s.find("\n---", m.end())
        if "adhar.io/plane:" in s[m.end():block_end]:
            continue
        s = s[: m.end()] + "    adhar.io/plane: control\n" + s[m.end():]
    open(path, "w").write(s)


if __name__ == "__main__":
    main(sys.argv[1] if len(sys.argv) > 1 else "manifests/install.yaml")
