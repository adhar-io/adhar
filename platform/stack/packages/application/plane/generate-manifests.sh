#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.8.0"

echo "# PLANE INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/plane/generate-manifests.sh'" >>${INSTALL_YAML}

helm repo add plane https://helm.plane.so --force-update
helm repo update
helm template --namespace adhar-system plane plane/plane-ce -f values.yaml --version ${CHART_VERSION} >>${INSTALL_YAML}

# The chart stamps every pod template with `timestamp: "<helm template run time>"`
# to force a rollout on `helm upgrade`. Under GitOps that is pure churn -- and on
# the migrator Job it is fatal: plane-api-migrate-1 keeps its name across renders,
# a Job's spec.template is immutable, and ArgoCD then retries the update forever
# with `field is immutable`, wedging the whole Application. Pin it so a
# regeneration only shows real changes.
#
# Consequence: a change confined to plane-app-vars (WEB_URL / CORS_ALLOWED_ORIGINS)
# no longer rolls the pods by itself -- follow such a change with
#   kubectl -n adhar-system rollout restart deploy -l app.kubernetes.io/instance=plane
sed -i '' -E 's/^([[:space:]]*)timestamp: "[^"]*"$/\1timestamp: "pinned-by-generate-manifests"/' ${INSTALL_YAML} \
  || sed -i -E 's/^([[:space:]]*)timestamp: "[^"]*"$/\1timestamp: "pinned-by-generate-manifests"/' ${INSTALL_YAML}

# The migrator is a plain Job in the chart: it runs once, and if the database
# is not up yet on a fresh cluster it exhausts backoffLimit ("timed out waiting
# for the condition") and stays Failed forever — ArgoCD then reports the whole
# Application Degraded on every sync (2026-09-15 DO bring-up). Migrations are
# idempotent, so make it a Sync hook that is recreated on every sync: a failed
# run is retried on the next sync instead of wedging the app.
python3 - "${INSTALL_YAML}" <<'PYEOF'
import re, sys
path = sys.argv[1]
src = open(path).read()
hook = ('  name: plane-api-migrate-1\n'
        '  annotations:\n'
        '    argocd.argoproj.io/hook: Sync\n'
        '    argocd.argoproj.io/hook-delete-policy: BeforeHookCreation\n')
out, n = re.subn(r'(?m)^  name: plane-api-migrate-1\n', hook, src, count=1)
open(path, 'w').write(out)
print(f"migrator Job marked as a Sync hook ({n})")
PYEOF

# Pin the bundled MinIO images to the registry the platform already pulls from.
#
# The chart ships `minio/minio:latest` and `minio/mc:latest` from Docker Hub.
# Both fail on a real cluster: the tags are unauthenticated Docker Hub pulls
# subject to rate limits, and plane-minio-wl-0 sat in ImagePullBackOff for hours
# (541 attempts) while the platform's OWN MinIO -- the same software, pulled as
# quay.io/minio/minio:RELEASE.2024-12-18T13-15-44Z -- started first time.
#
# Same images, a registry that answers, and a pinned release rather than a
# floating tag, which is what the supply-chain policy wants anyway.
python3 - ${INSTALL_YAML} <<'PYEOF'
import re, sys

path = sys.argv[1]
src = open(path).read()
subs = {
    r'(?<![\w/.-])minio/minio:latest\b': 'quay.io/minio/minio:RELEASE.2024-12-18T13-15-44Z',
    r'(?<![\w/.-])minio/mc:latest\b': 'quay.io/minio/mc:RELEASE.2024-11-21T17-21-54Z',
}
total = 0
for pattern, replacement in subs.items():
    src, n = re.subn(pattern, replacement, src)
    print(f"  {pattern} -> {replacement} ({n})")
    total += n
open(path, 'w').write(src)
print(f"pinned {total} MinIO image reference(s) to quay.io")
PYEOF

# Use the platform's OWN Plane build — every image, not just the backend.
#
# adhar-io/plane is the fork the platform's Plane changes live in: a Keycloak /
# generic-OIDC login provider in the backend AND the sign-in button for it in the
# web, space and admin front ends. The stock frontend cannot render a method it
# does not know, so running the fork backend under the upstream frontend (as this
# package did until 1.4.2-2) left the sign-in page unable to show Keycloak at all.
# The fork's CI publishes all six images under one tag — the upstream line plus a
# build number (1.4.2-2 for v1.4.2) — so the tag cannot come from the chart's
# `planeVersion` value and is rewritten here, the same way the MinIO images are.
#
# Verified public and multi-arch (linux/amd64 + linux/arm64) before pinning:
#   curl -H "Authorization: Bearer $(ghcr token)" \
#     https://ghcr.io/v2/adhar-io/plane-frontend/manifests/1.4.2-2
python3 - ${INSTALL_YAML} <<'PYEOF'
import re, sys

path = sys.argv[1]
tag = "1.4.2-3"
src = open(path).read()
total = 0
for name in ("backend", "frontend", "admin", "space", "live"):
    src, n = re.subn(rf'(?:artifacts\.plane\.so/makeplane|ghcr\.io/adhar-io)/plane-{name}:\S+',
                     f'ghcr.io/adhar-io/plane-{name}:{tag}', src)
    print(f"  plane-{name}: {n} reference(s) -> ghcr.io/adhar-io/plane-{name}:{tag}")
    total += n
if total < 8:
    raise SystemExit(f"rewrote only {total} Plane image references; expected the API, worker, beat, "
                     "migrator, web, space, admin and live workloads")
open(path, 'w').write(src)
PYEOF

# Every workload that reads `plane-doc-store-secrets` also reads the ESO-managed
# `plane-s3-credentials`.
#
# The chart renders the doc-store Secret with the bucket, endpoint, region and
# USE_MINIO=0, but with NO credentials: `env.aws_access_key` and
# `env.aws_secret_access_key` are deliberately left empty in values.yaml so the
# object store's root credential never enters git (see manifests/platform-storage.yaml).
# ArgoCD owns the chart's Secret, so the credentials cannot be merged into it —
# they arrive as a second envFrom entry, added here.
#
# Pinned by TestPlaneUsesThePlatformObjectStore: a regeneration that loses this
# step leaves every Plane workload without S3 credentials, and the symptom is an
# upload that fails at runtime rather than a pod that fails to start.
python3 - ${INSTALL_YAML} <<'PYEOF'
import re, sys

path = sys.argv[1]
src = open(path).read()

# Match the envFrom entry for the chart's doc-store Secret, at whatever
# indentation the template used, and append a sibling entry for ours.
pattern = re.compile(
    r'(?P<indent>[ ]*)- secretRef:\n'
    r'(?P<inner>[ ]*)name:[ ]+plane-doc-store-secrets\n'
    r'(?P<optindent>[ ]*)optional: false\n')


def add_credentials(m):
    return (m.group(0)
            + f"{m.group('indent')}- secretRef:\n"
            + f"{m.group('inner')}name: plane-s3-credentials\n"
            + f"{m.group('optindent')}optional: false\n")


src, n = pattern.subn(add_credentials, src)
# Every workload that mounts the doc-store Secret must have been matched. The
# chart's indentation is not uniform (one entry writes `name:` with two spaces),
# and a stricter pattern silently skipped that workload — which is the failure
# this check exists to catch, since the result is a pod that starts fine and
# cannot write to the object store.
mounts = len(re.findall(r'[ ]+name:[ ]+plane-doc-store-secrets\n[ ]+optional:', src))
if n == 0 or n != mounts:
    raise SystemExit(f"matched {n} of {mounts} plane-doc-store-secrets envFrom entries: the chart's "
                     "secret wiring changed, and some Plane workloads would run without S3 credentials")
open(path, 'w').write(src)
print(f"added the platform S3 credentials to {n} workload(s)")
PYEOF

# Raise the chart's one-second probe timeout.
#
# The chart ships `timeoutSeconds: 1` on the API readiness probe. On a loaded
# node that kills a healthy pod — the platform rule is that no package may ship a
# one-second probe, enforced by TestNoProbeShipsAOneSecondTimeout.
#
# This used to be a hand-edit of the generated file, which meant the FIRST
# regeneration silently undid it (2026-10-08). Doing it here is the only form
# that survives.
python3 - ${INSTALL_YAML} <<'PYEOF'
import re, sys

path = sys.argv[1]
src = open(path).read()
src, n = re.subn(r'(?m)^([ ]*)timeoutSeconds: 1$', r'\g<1>timeoutSeconds: 5', src)
open(path, 'w').write(src)
print(f"raised {n} one-second probe timeout(s) to 5s")
PYEOF

# Start the Keycloak sign-in without a click when it is the only method.
#
# The fork's sign-in page always waits for a click on "Login with Keycloak".
# The HTTPRoute covers a visitor with no cookie, but one holding an ANONYMOUS
# Plane session (left by any abandoned or failed sign-in) lands on that page
# and stays there. auto-sso.js (beside this script) is loaded first by the
# SPA's index.html: an init container in the SAME image copies the image's own
# index.html and adds the <script> tag, so the patch follows every image bump
# instead of pinning one build's hashed asset names. If the tag cannot be
# inserted the original page is served unchanged — a missing convenience, never
# a broken sign-in. The durable fix is in the fork's frontend
# (apps/web/components/account/auth-forms/auth-root.tsx); drop this once a
# release carries it.
python3 - ${INSTALL_YAML} "$(dirname "$0")/auto-sso.js" <<'PYEOF'
import sys
path, script = sys.argv[1], open(sys.argv[2]).read()
src = open(path).read()
anchor = ("      containers:\n"
          "      - name: plane-web\n")
if src.count(anchor) != 1:
    raise SystemExit("plane-web container not found exactly once: the chart's web Deployment changed shape")
init = r"""      initContainers:
      - name: auto-sso-index
        image: IMAGE
        command:
        - sh
        - -c
        - |
          src=/usr/share/caddy/html/index.html
          sed 's#<head\([^>]*\)>#<head\1><script src="/adhar-auto-sso.js"></script>#' "$src" > /patched/index.html
          if grep -q adhar-auto-sso /patched/index.html; then echo "index.html: auto sign-in script added"
          else cp "$src" /patched/index.html; echo "index.html: no <head> found, served unchanged"; fi
        resources:
          requests: {cpu: 5m, memory: 16Mi}
          limits: {memory: 64Mi}
        volumeMounts:
        - {name: auto-sso-index, mountPath: /patched}
"""
i = src.index(anchor)
img_line = src.index("        image: ", i)
image = src[img_line:src.index("\n", img_line)].split("image: ", 1)[1].strip()
src = src[:i] + init.replace("IMAGE", image) + src[i:]
mounts = """        volumeMounts:
        - {name: auto-sso-index, mountPath: /usr/share/caddy/html/index.html, subPath: index.html}
        - {name: auto-sso-script, mountPath: /usr/share/caddy/html/adhar-auto-sso.js, subPath: auto-sso.js}
"""
end = src.index("      serviceAccount: plane-srv-account\n", i)
vols = """      volumes:
      - {name: auto-sso-index, emptyDir: {}}
      - name: auto-sso-script
        configMap: {name: plane-web-auto-sso}
"""
src = src[:end] + mounts + vols + src[end:]
indented = "".join("    " + l + "\n" if l else "\n" for l in script.splitlines())
src = src.rstrip("\n") + "\n---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: plane-web-auto-sso\n  namespace: adhar-system\ndata:\n  auto-sso.js: |\n" + indented
open(path, "w").write(src)
print("plane-web: Keycloak sign-in starts on its own when it is the only method")
PYEOF
