#!/usr/bin/env python3
"""Edits the litmus chart cannot express, re-applied after every `helm template`
by generate-manifests.sh. Text-level and idempotent.

Give the auth server its OIDC settings (manifests/oidc.yaml): the chart's
auth-server envFrom names only litmus-admin-secret and litmus-admin-config, and
offers no way to add a source.
"""
import sys

ANCHOR = """        envFrom:
        - secretRef:
            name: litmus-admin-secret
        - configMapRef:
            name: litmus-admin-config
"""
ADD = """        - secretRef:
            name: litmus-oidc
"""


def main(path):
    s = open(path).read()
    i = s.find("\n  name: litmus-auth-server\n")
    if i < 0:
        sys.exit(f"post-render: litmus-auth-server Deployment not found in {path}")
    j = s.find(ANCHOR, i)
    end = s.find("\n---", i)
    if j < 0 or (end >= 0 and j > end):
        sys.exit(f"post-render: the auth-server envFrom changed shape in {path}; re-check the chart")
    k = j + len(ANCHOR)
    if s[k:k + len(ADD)] == ADD:
        return
    open(path, "w").write(s[:k] + ADD + s[k:])


if __name__ == "__main__":
    main(sys.argv[1] if len(sys.argv) > 1 else "manifests/install.yaml")
