#!/usr/bin/env python3
"""Edits the penpot chart (1.9.0) cannot express, re-applied after every
`helm template` by generate-manifests.sh. Text-level and idempotent, so the
rest of the rendered file stays byte-for-byte what the chart produced.

Wait for Keycloak before the backend starts. Penpot configures its OIDC
provider ONCE at boot by fetching `<PENPOT_OIDC_BASE_URI>/.well-known/
openid-configuration` and never retries. On a fresh cluster Keycloak's
hostname does not resolve yet when Penpot boots, so the provider was dropped
("uri host could not be resolved"), every SSO attempt then failed with
`sso-provider-not-configured`, and sign-in stayed broken until someone
restarted the backend (AWS, 2026-10-24). The wait is bounded: if Keycloak
never answers (SSO switched off), Penpot still starts with password login.
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
            - name: PENPOT_OIDC_BASE_URI
              value: "https://keycloak.adhar.localtest.me:8443/realms/adhar/"
          command: ["/bin/sh", "-c"]
          args:
            - |
              url="${PENPOT_OIDC_BASE_URI%/}/.well-known/openid-configuration"
              i=0
              until curl -ksf -o /dev/null --max-time 5 "$url"; do
                i=$((i+1))
                if [ "$i" -ge 180 ]; then
                  echo "Keycloak discovery not reachable after 15 min; starting without SSO"
                  exit 0
                fi
                echo "waiting for $url ($i)"
                sleep 5
              done
              echo "Keycloak discovery reachable: $url"
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


def main(path):
    s = open(path).read()
    if MARKER in s:
        return
    if s.count(ANCHOR) != 1:
        sys.exit(f"post-render: backend pod spec anchor not found exactly once in {path}; the chart changed")
    open(path, "w").write(s.replace(ANCHOR, INIT))


if __name__ == "__main__":
    main(sys.argv[1] if len(sys.argv) > 1 else "manifests/install.yaml")
