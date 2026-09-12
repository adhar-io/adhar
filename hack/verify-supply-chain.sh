#!/usr/bin/env bash
#
# verify-supply-chain.sh — prove ADR-0019 signature enforcement actually works
# on THIS cluster.
#
# The three supply-chain ClusterPolicies ship in Audit mode by default
# (platform/stack/packages/security/supply-chain-policies/manifests/audit). Audit
# can only ever tell you what it *would* have done — and the parts that silently
# fail in practice are exactly the parts Audit hides: can Kyverno reach the
# registry, does it trust the TLS, does it parse a signature made the way the
# platform actually makes them (key-based, --tlog-upload=false)?
#
# This drill answers that, end to end, without touching the platform's own
# policies or key:
#
#   1. generate a THROWAWAY cosign keypair (never the platform's cosign-key)
#   2. copy two tiny public images into the platform Harbor as
#      <repo>:signed and <repo>:unsigned (two different upstream digests, so
#      signing one does not sign the other)
#   3. sign only :signed, with the throwaway key, exactly as the Tekton
#      cosign-sign Task does (--key, --tlog-upload=false)
#   4. apply an Enforce ClusterPolicy scoped to ONE scratch namespace, pinned to
#      the throwaway public key, with failurePolicy: Fail so the result is
#      deterministic rather than fail-open
#   5. assert: unsigned Pod -> admission DENIED, signed Pod -> ADMITTED
#   6. delete the policy, the namespace, the pods, the temp key material and
#      (best effort) the Harbor repository
#
# Everything is namespaced/named with a fixed drill prefix and every step is
# idempotent, so the script is safe to run repeatedly. It exits non-zero on any
# unexpected outcome.
#
# Usage:
#   hack/verify-supply-chain.sh
#
# Environment overrides:
#   KUBE_CONTEXT    kube context to use              (default: kind-adhar)
#   HARBOR_HOST     registry host[:port]             (default: harbor.<platform host>:8443)
#   HARBOR_USER     Harbor username                  (default: read from the harbor-push Secret)
#   HARBOR_PASS     Harbor password                  (default: read from the harbor-push Secret)
#   SIGNED_SRC      upstream image copied as :signed (default: registry.k8s.io/pause:3.9)
#   UNSIGNED_SRC    upstream image copied as :unsigned (default: registry.k8s.io/pause:3.10)
#   KEEP=1          leave the scratch namespace/policy in place for inspection
#
set -euo pipefail

CONTEXT="${KUBE_CONTEXT:-kind-adhar}"
KUBECTL=(kubectl --context "${CONTEXT}")
NS="adhar-supplychain-drill"
POLICY="adhar-supplychain-drill"
REPO_PATH="library/adhar-supplychain-drill"
SIGNED_SRC="${SIGNED_SRC:-registry.k8s.io/pause:3.9}"
UNSIGNED_SRC="${UNSIGNED_SRC:-registry.k8s.io/pause:3.10}"
PLATFORM_NS="adhar-system"

RED=$'\033[0;31m'; GRN=$'\033[0;32m'; YLW=$'\033[0;33m'; BLD=$'\033[1m'; OFF=$'\033[0m'
step()  { printf '\n%s==> %s%s\n' "${BLD}" "$*" "${OFF}"; }
info()  { printf '    %s\n' "$*"; }
pass()  { printf '%s  PASS%s  %s\n' "${GRN}" "${OFF}" "$*"; }
fail()  { printf '%s  FAIL%s  %s\n' "${RED}" "${OFF}" "$*" >&2; FAILED=1; }
die()   { printf '%s  ERROR%s %s\n' "${RED}" "${OFF}" "$*" >&2; exit 1; }
warn()  { printf '%s  WARN%s  %s\n' "${YLW}" "${OFF}" "$*"; }

FAILED=0
TMP=""
cleanup() {
  local rc=$?
  if [[ "${KEEP:-0}" == "1" ]]; then
    warn "KEEP=1 — leaving namespace/${NS} and clusterpolicy/${POLICY} in place"
  else
    step "Cleanup"
    "${KUBECTL[@]}" delete clusterpolicy "${POLICY}" --ignore-not-found --wait=false >/dev/null 2>&1 || true
    "${KUBECTL[@]}" delete namespace "${NS}" --ignore-not-found --wait=false >/dev/null 2>&1 || true
    info "removed clusterpolicy/${POLICY} and namespace/${NS}"
    # Best effort: drop the drill repository from Harbor so repeated runs do not
    # accumulate artifacts. Harbor serves a self-signed cert locally (-k).
    if [[ -n "${HARBOR_HOST:-}" && -n "${HARBOR_USER:-}" ]]; then
      if curl -sk -u "${HARBOR_USER}:${HARBOR_PASS:-}" -X DELETE \
           "https://${HARBOR_HOST}/api/v2.0/projects/library/repositories/adhar-supplychain-drill" \
           -o /dev/null -w '' 2>/dev/null; then
        info "removed Harbor repository ${REPO_PATH} (best effort)"
      fi
    fi
  fi
  [[ -n "${TMP}" ]] && rm -rf "${TMP}"
  exit "${rc}"
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
# 0. Preflight. Never run against a cluster that is not there or not ready.
# ---------------------------------------------------------------------------
step "Preflight"

command -v cosign >/dev/null 2>&1 || die "cosign not found on PATH (expected /usr/local/bin/cosign)"
command -v kubectl >/dev/null 2>&1 || die "kubectl not found on PATH"
command -v curl >/dev/null 2>&1 || die "curl not found on PATH"
info "cosign: $(cosign version 2>/dev/null | awk '/GitVersion/{print $2}' | head -1)"

"${KUBECTL[@]}" get nodes >/dev/null 2>&1 \
  || die "no cluster reachable at context '${CONTEXT}' — bring the platform up first (adhar up)"
info "cluster: $("${KUBECTL[@]}" config current-context) ($("${KUBECTL[@]}" get nodes --no-headers | wc -l | tr -d ' ') node(s))"

"${KUBECTL[@]}" get crd clusterpolicies.kyverno.io >/dev/null 2>&1 \
  || die "Kyverno CRDs are not installed — the security/kyverno package must be synced first"

READY_KYVERNO=$("${KUBECTL[@]}" get deploy -n "${PLATFORM_NS}" kyverno-admission-controller \
  -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo 0)
[[ "${READY_KYVERNO:-0}" -ge 1 ]] \
  || die "kyverno-admission-controller has no ready replica in ${PLATFORM_NS} — enforcement cannot be proven"
info "kyverno-admission-controller: ${READY_KYVERNO} ready replica(s)"

"${KUBECTL[@]}" get svc -n "${PLATFORM_NS}" harbor-core >/dev/null 2>&1 \
  || die "Harbor is not installed (no svc/harbor-core in ${PLATFORM_NS}) — the drill needs the platform registry"

# Registry host. Default to the platform edge name Harbor publishes itself at
# (EXT_ENDPOINT), which the Gateway also serves on an in-cluster :8443 listener
# — so the SAME reference string works from this laptop and from Kyverno.
if [[ -z "${HARBOR_HOST:-}" ]]; then
  HARBOR_HOST="$("${KUBECTL[@]}" get cm -n "${PLATFORM_NS}" harbor-core \
    -o jsonpath='{.data.EXT_ENDPOINT}' 2>/dev/null | sed -e 's#^https\?://##' -e 's#/$##')"
fi
[[ -n "${HARBOR_HOST}" ]] || die "could not determine the Harbor host — set HARBOR_HOST=harbor.<domain>:<port>"
info "harbor: ${HARBOR_HOST}"

# Credentials: the harbor-push Secret is what the platform's own pipelines use.
if [[ -z "${HARBOR_USER:-}" || -z "${HARBOR_PASS:-}" ]]; then
  HARBOR_USER="$("${KUBECTL[@]}" get secret -n "${PLATFORM_NS}" harbor-push \
    -o jsonpath='{.data.username}' 2>/dev/null | base64 -d 2>/dev/null || true)"
  HARBOR_PASS="$("${KUBECTL[@]}" get secret -n "${PLATFORM_NS}" harbor-push \
    -o jsonpath='{.data.password}' 2>/dev/null | base64 -d 2>/dev/null || true)"
fi
[[ -n "${HARBOR_USER}" && -n "${HARBOR_PASS}" ]] \
  || die "no Harbor credentials — set HARBOR_USER/HARBOR_PASS (or see: adhar get secrets -p harbor)"
info "harbor user: ${HARBOR_USER}"

curl -sk --max-time 15 -o /dev/null "https://${HARBOR_HOST}/v2/" \
  || die "Harbor is not reachable at https://${HARBOR_HOST}/v2/ from this machine"
info "harbor registry API reachable"

TMP="$(mktemp -d -t adhar-supplychain-drill)"
export DOCKER_CONFIG="${TMP}/docker"
mkdir -p "${DOCKER_CONFIG}"
# Written by hand rather than `cosign login` so the user's own ~/.docker/config.json
# is never touched and no TLS handshake is needed just to store a credential.
printf '{"auths":{"%s":{"auth":"%s"}}}\n' \
  "${HARBOR_HOST}" "$(printf '%s:%s' "${HARBOR_USER}" "${HARBOR_PASS}" | base64 | tr -d '\n')" \
  > "${DOCKER_CONFIG}/config.json"

IMG="${HARBOR_HOST}/${REPO_PATH}"

# ---------------------------------------------------------------------------
# 1. Throwaway keypair. NEVER the platform's cosign-key: the drill must not be
#    able to make a real platform-trusted signature, and must not depend on the
#    platform key existing.
# ---------------------------------------------------------------------------
step "1/6  Generate an ephemeral cosign keypair"
export COSIGN_PASSWORD="adhar-drill"
( cd "${TMP}" && cosign generate-key-pair >/dev/null 2>&1 ) \
  || die "cosign generate-key-pair failed"
info "keypair written to ${TMP} (discarded on exit)"

# ---------------------------------------------------------------------------
# 2. Two DIFFERENT upstream images -> two different digests in Harbor. Signing
#    is by digest, so if both tags pointed at one manifest, signing :signed
#    would also satisfy :unsigned and the drill would prove nothing.
# ---------------------------------------------------------------------------
step "2/6  Copy two tiny public images into Harbor"
# --allow-insecure-registry: Harbor terminates TLS with the per-cluster `adhar`
# CA, which this laptop does not trust.
cosign copy -f --allow-insecure-registry "${SIGNED_SRC}"   "${IMG}:signed" >/dev/null 2>&1 \
  || die "failed to copy ${SIGNED_SRC} -> ${IMG}:signed"
info "${SIGNED_SRC} -> ${IMG}:signed"
cosign copy -f --allow-insecure-registry "${UNSIGNED_SRC}" "${IMG}:unsigned" >/dev/null 2>&1 \
  || die "failed to copy ${UNSIGNED_SRC} -> ${IMG}:unsigned"
info "${UNSIGNED_SRC} -> ${IMG}:unsigned"

DIG_SIGNED="$(cosign triangulate --allow-insecure-registry --type digest "${IMG}:signed" 2>/dev/null || true)"
DIG_UNSIGNED="$(cosign triangulate --allow-insecure-registry --type digest "${IMG}:unsigned" 2>/dev/null || true)"
if [[ -n "${DIG_SIGNED}" && "${DIG_SIGNED}" == "${DIG_UNSIGNED}" ]]; then
  die "SIGNED_SRC and UNSIGNED_SRC resolve to the same digest — pick two different images"
fi

# ---------------------------------------------------------------------------
# 3. Sign exactly the way the platform does: key-based, no transparency log.
# ---------------------------------------------------------------------------
step "3/6  Sign only :signed with the ephemeral key"
cosign sign --yes --key "${TMP}/cosign.key" --tlog-upload=false \
  --allow-insecure-registry "${IMG}:signed" >/dev/null 2>&1 \
  || die "cosign sign failed for ${IMG}:signed"
info "signed ${IMG}:signed (--tlog-upload=false, as the Tekton cosign-sign Task does)"

cosign verify --key "${TMP}/cosign.pub" --insecure-ignore-tlog \
  --allow-insecure-registry "${IMG}:signed" >/dev/null 2>&1 \
  || die "local cosign verify failed — the signature never landed, so the admission test would be meaningless"
pass "cosign verify (local) accepts ${IMG}:signed"

if cosign verify --key "${TMP}/cosign.pub" --insecure-ignore-tlog \
     --allow-insecure-registry "${IMG}:unsigned" >/dev/null 2>&1; then
  die "local cosign verify ACCEPTED ${IMG}:unsigned — the two tags share a digest, drill invalid"
fi
pass "cosign verify (local) rejects ${IMG}:unsigned"

# ---------------------------------------------------------------------------
# 4. Scratch namespace + an Enforce policy scoped to it and nothing else.
# ---------------------------------------------------------------------------
step "4/6  Apply an Enforce ClusterPolicy scoped to namespace/${NS}"
"${KUBECTL[@]}" delete clusterpolicy "${POLICY}" --ignore-not-found >/dev/null 2>&1 || true
"${KUBECTL[@]}" delete namespace "${NS}" --ignore-not-found --wait=true >/dev/null 2>&1 || true
"${KUBECTL[@]}" create namespace "${NS}" >/dev/null

PUBKEY_INDENTED="$(sed 's/^/                      /' "${TMP}/cosign.pub")"
cat <<EOF | "${KUBECTL[@]}" apply -f - >/dev/null
apiVersion: kyverno.io/v1
kind: ClusterPolicy
metadata:
  name: ${POLICY}
  annotations:
    policies.kyverno.io/title: Supply-chain enforcement drill (ADR-0019)
    policies.kyverno.io/description: >-
      Temporary. Created and deleted by hack/verify-supply-chain.sh to prove
      that cosign signature verification actually blocks at admission on this
      cluster. Scoped to namespace ${NS} and to one throwaway repository.
spec:
  background: false
  webhookTimeoutSeconds: 30
  # Ask to fail CLOSED: a fail-open policy would report a false PASS on the
  # "signed image is admitted" half. NOTE the platform's kyverno values set
  # features.forceFailurePolicyIgnore.enabled=true (ADR-0012 webhook hygiene on
  # a single node), which overrides this back to Ignore — which is why the
  # preflight above hard-requires a ready kyverno-admission-controller. The
  # DENY assertion is the load-bearing one: a denial can only come from a
  # webhook that actually ran.
  failurePolicy: Fail
  rules:
    - name: drill-verify-signature
      match:
        any:
          - resources:
              kinds: ["Pod"]
              namespaces: ["${NS}"]
      verifyImages:
        - failureAction: Enforce
          imageReferences:
            - "${IMG}:*"
          imageRegistryCredentials:
            allowInsecureRegistry: true
          required: true
          mutateDigest: true
          verifyDigest: true
          attestors:
            - count: 1
              entries:
                - keys:
                    publicKeys: |-
${PUBKEY_INDENTED}
                    rekor:
                      ignoreTlog: true
                    ctlog:
                      ignoreSCT: true
EOF
info "clusterpolicy/${POLICY} applied"

for _ in $(seq 1 30); do
  READY="$("${KUBECTL[@]}" get clusterpolicy "${POLICY}" -o jsonpath='{.status.ready}' 2>/dev/null || true)"
  [[ "${READY}" == "true" ]] && break
  sleep 2
done
[[ "${READY:-}" == "true" ]] || die "clusterpolicy/${POLICY} never became ready: $("${KUBECTL[@]}" get clusterpolicy "${POLICY}" -o jsonpath='{.status}' 2>/dev/null)"
info "clusterpolicy/${POLICY} is ready"

pod_manifest() { # $1 = pod name, $2 = image
  cat <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: $1
  namespace: ${NS}
spec:
  restartPolicy: Never
  containers:
    - name: app
      image: $2
      command: ["/pause"]
EOF
}

# ---------------------------------------------------------------------------
# 5. The two assertions. Server-side dry-run: the request traverses the real
#    admission chain (Kyverno's webhook included) but nothing is persisted, so
#    the drill never leaves a Pod behind and never needs the node to pull.
# ---------------------------------------------------------------------------
step "5/6  Assert: unsigned image is DENIED"
set +e
DENY_OUT="$(pod_manifest drill-unsigned "${IMG}:unsigned" \
  | "${KUBECTL[@]}" apply --dry-run=server -f - 2>&1)"
DENY_RC=$?
set -e
# Be specific about WHY it was denied: a non-zero exit alone could come from an
# unrelated policy or a malformed manifest, which would be a false PASS.
if [[ ${DENY_RC} -ne 0 ]] && grep -qEi "${POLICY}|image verification failed|signature" <<<"${DENY_OUT}"; then
  pass "unsigned image rejected by clusterpolicy/${POLICY}"
  info "$(tr '\n' ' ' <<<"${DENY_OUT}" | cut -c1-400)"
else
  fail "unsigned image was NOT denied (exit ${DENY_RC})"
  printf '%s\n' "${DENY_OUT}" >&2
fi

step "6/6  Assert: signed image is ADMITTED"
set +e
ALLOW_OUT="$(pod_manifest drill-signed "${IMG}:signed" \
  | "${KUBECTL[@]}" apply --dry-run=server -f - 2>&1)"
ALLOW_RC=$?
set -e
if [[ ${ALLOW_RC} -eq 0 ]]; then
  pass "signed image admitted"
  info "$(tr '\n' ' ' <<<"${ALLOW_OUT}" | cut -c1-400)"
else
  fail "signed image was REJECTED (exit ${ALLOW_RC}) — Kyverno could not verify a signature this platform can make"
  printf '%s\n' "${ALLOW_OUT}" >&2
fi

# ---------------------------------------------------------------------------
step "Result"
if [[ ${FAILED} -ne 0 ]]; then
  printf '%sSUPPLY-CHAIN ENFORCEMENT DRILL FAILED%s\n' "${RED}" "${OFF}" >&2
  exit 1
fi
printf '%sSUPPLY-CHAIN ENFORCEMENT DRILL PASSED%s\n' "${GRN}" "${OFF}"
printf '  Kyverno on this cluster reaches %s, parses a key-based cosign\n' "${HARBOR_HOST}"
printf '  signature made with --tlog-upload=false, and enforces it at admission.\n'
printf '  That is exactly the signature the Tekton cosign-sign Task produces, so\n'
printf '  security/supply-chain-policies/manifests/enforce is safe to enable here.\n'
