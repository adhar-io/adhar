#!/usr/bin/env python3
"""Edits the penpot chart (1.9.0) cannot express, re-applied after every
`helm template` by generate-manifests.sh. Text-level and idempotent, so the
rest of the rendered file stays byte-for-byte what the chart produced.

Wait for Keycloak before the backend starts. Penpot sets up its OIDC provider
ONCE at boot and never retries. It used to discover the provider from the
PUBLIC issuer URL, and on a fresh cluster that hostname did not resolve yet,
so the provider was dropped ("uri host could not be resolved"), every SSO
attempt failed with `sso-provider-not-configured`, and sign-in stayed broken
until someone restarted the backend (AWS, 2026-10-24).

Since 2026-10-26 the auth, token and user URIs are all configured
(values.yaml), so Penpot skips discovery and the only boot-time call is the
JWKS fetch — to the IN-CLUSTER Keycloak Service, which is what this waits for
(no public DNS, no Gateway, no certificate trust involved). A JWKS that is
missing at boot no longer disables SSO (Penpot falls back to userinfo), so
the wait only keeps ID-token verification local. It is bounded: if Keycloak
never answers (SSO switched off), Penpot still starts.

Drop the literal "$PENPOT_FLAGS " the chart prepends to config.flags on the
backend and the frontend. Kubernetes expands only $(VAR), so the placeholder
reached the env — and the frontend's /js/config.js — verbatim. Both flag
parsers skip it, but it is not a flag, and reading the live env should show
exactly what values.yaml asks for (enable-access-tokens included).
"""
import sys

MARKER = "name: wait-for-keycloak"
ANCHOR = """      serviceAccountName: penpot
      securityContext:
        fsGroup: 1001
      containers:
        - name: penpot-backend
"""
INIT = """      serviceAccountName: penpot
      securityContext:
        fsGroup: 1001
      initContainers:
        - name: wait-for-keycloak
          image: docker.io/curlimages/curl:8.11.1
          env:
            - name: KEYCLOAK_JWKS_URI
              value: "http://keycloak.adhar-system.svc.cluster.local:8080/realms/adhar/protocol/openid-connect/certs"
          command: ["/bin/sh", "-c"]
          args:
            - |
              i=0
              until curl -sf -o /dev/null --max-time 5 "$KEYCLOAK_JWKS_URI"; do
                i=$((i+1))
                if [ "$i" -ge 180 ]; then
                  echo "Keycloak JWKS not reachable after 15 min; starting anyway (userinfo fallback)"
                  exit 0
                fi
                echo "waiting for $KEYCLOAK_JWKS_URI ($i)"
                sleep 5
              done
              echo "Keycloak JWKS reachable: $KEYCLOAK_JWKS_URI"
          resources:
            requests: {cpu: 10m, memory: 16Mi}
            limits: {memory: 32Mi}
          securityContext:
            allowPrivilegeEscalation: false
            runAsNonRoot: true
            # Numeric: the image's user is the NAME curl_user, which kubelet
            # cannot verify as non-root (CreateContainerConfigError).
            runAsUser: 100
            capabilities: {drop: [all]}
      containers:
        - name: penpot-backend
"""


# Both Deployments that read PENPOT_FLAGS (backend, frontend) render it as
#   - name: PENPOT_FLAGS
#     value: "$PENPOT_FLAGS <config.flags>"
FLAGS_ENV = "- name: PENPOT_FLAGS\n"
FLAGS_PLACEHOLDER = 'value: "$PENPOT_FLAGS '


def wait_for_keycloak(s, path):
    if MARKER in s:
        return s
    if s.count(ANCHOR) != 1:
        sys.exit(f"post-render: backend pod spec anchor not found exactly once in {path}; the chart changed")
    return s.replace(ANCHOR, INIT)


def strip_flags_placeholder(s, path):
    if s.count(FLAGS_ENV) != 2:
        sys.exit(f"post-render: PENPOT_FLAGS is not set on exactly the backend and the frontend in {path}; the chart changed")
    s = s.replace(FLAGS_PLACEHOLDER, 'value: "')
    if "$PENPOT_FLAGS" in s:
        sys.exit(f"post-render: a literal $PENPOT_FLAGS is left in {path}; the chart changed how it renders config.flags")
    return s


def main(path):
    s = open(path).read()
    out = strip_flags_placeholder(wait_for_keycloak(s, path), path)
    if out != s:
        open(path, "w").write(out)


if __name__ == "__main__":
    main(sys.argv[1] if len(sys.argv) > 1 else "manifests/install.yaml")
