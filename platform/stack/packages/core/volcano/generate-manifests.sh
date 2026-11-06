#!/bin/bash
set -euo pipefail

# Volcano, rendered into adhar-system (ADR-0011, ADR-0028, docs/design/0025 §4.1).
#
# Writes manifests/install.yaml only. manifests/admission-certs.yaml and
# manifests/servicemonitor.yaml are hand-written and stay as they are.
#
# Needs helm and python3 with PyYAML. The chart comes from the upstream release
# asset and is checked against the digest below, so a re-tagged release fails here.
#
# Every edit below asserts its anchor exists. A chart bump that renames something
# stops the render instead of silently dropping the edit, and
# platform/controllers/adharplatform/volcano_package_test.go pins the outcome.
cd "$(dirname "${BASH_SOURCE[0]}")"

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.16.0"
CHART_URL="https://github.com/volcano-sh/helm-charts/releases/download/volcano-${CHART_VERSION}/volcano-${CHART_VERSION}.tgz"
CHART_SHA256="7f559fc8277e5023ae78b00a1b158b89eedf85afd27730f13511d1bb3527422d"

TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

curl -fsSL -o "${TMP}/volcano.tgz" "${CHART_URL}"
echo "${CHART_SHA256}  ${TMP}/volcano.tgz" | shasum -a 256 -c - >/dev/null

helm template --namespace adhar-system volcano "${TMP}/volcano.tgz" \
  -f values.yaml --version "${CHART_VERSION}" >"${TMP}/rendered.yaml"

python3 - "${TMP}/rendered.yaml" "${INSTALL_YAML}" "${CHART_VERSION}" <<'PYEOF'
import sys
import yaml

src, dst, version = sys.argv[1:4]
docs = [d for d in yaml.safe_load_all(open(src)) if d]


def take(pred, expect, why):
    """Remove the documents matching pred, insisting on how many there were."""
    global docs
    hit = [d for d in docs if pred(d)]
    if len(hit) != expect:
        names = [f"{d['kind']}/{d['metadata']['name']}" for d in hit]
        sys.exit(f"expected {expect} docs to remove ({why}), found {names}")
    docs = [d for d in docs if not pred(d)]


def only(kind, name):
    hit = [d for d in docs if d["kind"] == kind and d["metadata"]["name"] == name]
    if len(hit) != 1:
        sys.exit(f"expected exactly one {kind}/{name}, found {len(hit)}")
    return hit[0]


def annotate(doc, values):
    doc["metadata"].setdefault("annotations", {}).update(values)


def wave(doc, n):
    annotate(doc, {"argocd.argoproj.io/sync-wave": str(n)})


# JobFlow and JobTemplate: not installed, and together ≈0.5 MB of API server
# memory on a control plane that is already its tightest resource (ADR-0028).
# values.yaml turns their controllers off, or the informers would never sync.
take(
    lambda d: d["kind"] == "CustomResourceDefinition"
    and d["spec"]["group"] == "flow.volcano.sh",
    2,
    "JobFlow CRDs",
)

# The admission-init Job writes the serving certificate from a helm hook. Argo CD
# does not read helm hooks, and cert-manager owns that Secret here instead.
take(lambda d: d["metadata"]["name"].startswith("volcano-admission-init"), 4, "admission-init")

# Large CRDs (Job 228 KB, CronJob 260 KB, against a 256 KB last-applied limit):
# client-side apply cannot carry them. Before everything, or the rest cannot apply.
crds = [d for d in docs if d["kind"] == "CustomResourceDefinition"]
if len(crds) < 8:
    sys.exit(f"expected the core CRD set, found {len(crds)}")
for d in crds:
    annotate(d, {"argocd.argoproj.io/sync-options": "ServerSideApply=true"})
    wave(d, -5)

# Narrow the admission ClusterRole. Upstream can create and update every webhook
# configuration on the cluster and approve CSRs, which is cluster-admin with extra
# steps. The binary does two things with this API at startup, Get and Update of
# its OWN configurations to write the caBundle (cmd/webhook-manager/app/util.go),
# and no CSR call exists in v1.16.0: cert-manager issues the certificate.
role = only("ClusterRole", "volcano-admission")
before = len(role["rules"])
role["rules"] = [
    r
    for r in role["rules"]
    if r["apiGroups"] not in (["certificates.k8s.io"], ["admissionregistration.k8s.io"])
]
if before - len(role["rules"]) != 3:
    sys.exit("the admission ClusterRole no longer has the three rules being narrowed")

mutating = [d for d in docs if d["kind"] == "MutatingWebhookConfiguration"]
validating = [d for d in docs if d["kind"] == "ValidatingWebhookConfiguration"]
if not mutating or not validating:
    sys.exit("no webhook configurations rendered")
for configs, resource in (
    (mutating, "mutatingwebhookconfigurations"),
    (validating, "validatingwebhookconfigurations"),
):
    role["rules"].append(
        {
            "apiGroups": ["admissionregistration.k8s.io"],
            "resources": [resource],
            "resourceNames": sorted(d["metadata"]["name"] for d in configs),
            "verbs": ["get", "update"],
        }
    )

# cainjector fills the caBundle as soon as the Certificate is ready. The binary
# writes the same value from its mounted ca.crt, which is why it keeps `update`.
for d in mutating + validating:
    annotate(d, {"cert-manager.io/inject-ca-from": "adhar-system/volcano-admission"})
    # Same wave as the Deployment, which polls for up to five minutes for its
    # configurations to exist and only then opens the webhook port.
    wave(d, 0)

deployments = {d["metadata"]["name"]: d for d in docs if d["kind"] == "Deployment"}
if sorted(deployments) != ["volcano-admission", "volcano-controllers", "volcano-scheduler"]:
    sys.exit(f"unexpected Deployments: {sorted(deployments)}")


def probes(readiness, liveness):
    return {
        # Tolerant on purpose: no probe here may kill a healthy pod on a loaded node
        # (TestNoProbeShipsAOneSecondTimeout, CLAUDE.md "Probe timeouts").
        "readinessProbe": {
            **readiness,
            "periodSeconds": 10,
            "timeoutSeconds": 5,
            "failureThreshold": 3,
        },
        "livenessProbe": {
            **liveness,
            "initialDelaySeconds": 60,
            "periodSeconds": 30,
            "timeoutSeconds": 10,
            "failureThreshold": 5,
        },
    }


healthz_http = {"httpGet": {"path": "/healthz", "port": 11251}}
# Admission serves its health endpoint over TLS when it has certificates
# (helpers.StartHealthz), and starts that endpoint BEFORE the webhook server, which
# only listens once it has patched every webhook configuration. So health says the
# process is alive; only the webhook port says it can answer the API server. The
# chart ships no probes at all, which made the pod Ready before anything listened.
admission_probes = probes(
    {"tcpSocket": {"port": 8443}},
    {"httpGet": {"path": "/healthz", "port": 11251, "scheme": "HTTPS"}},
)
probe_for = {
    "volcano-admission": admission_probes,
    "volcano-controllers": probes(healthz_http, healthz_http),
    "volcano-scheduler": probes(healthz_http, healthz_http),
}

for name, d in deployments.items():
    pod = d["spec"]["template"]["spec"]
    # Shared namespace: every Service becomes <NAME>_PORT=tcp://… in this pod's env
    # (CONFLICTS.md §2).
    pod["enableServiceLinks"] = False
    # The chart hardcodes system-cluster-critical, value 2e9, which preempts anything
    # on a full node. Most stateful pods here sit on node-local volumes and can only
    # return to their own node, and a preempted Kafka broker stays down (2026-10-23).
    # A Pending scheduler is a capacity problem for the autoscaler, not a priority one.
    if pod.pop("priorityClassName", None) != "system-cluster-critical":
        sys.exit(f"{name}: the chart's priorityClassName has changed; re-read this edit")
    containers = pod["containers"]
    if len(containers) != 1:
        sys.exit(f"{name}: expected one container")
    containers[0].update(probe_for[name])

# The scheduler creates the `root` and `default` queues at startup, through the
# queue webhook, and exits if that keeps failing for a minute. Wave 0 holds it
# until admission reads Ready.
wave(deployments["volcano-admission"], 0)
wave(deployments["volcano-controllers"], 1)
wave(deployments["volcano-scheduler"], 1)

header = (
    "# VOLCANO (batch scheduler) INSTALL RESOURCES\n"
    "# This file is auto-generated with 'platform/stack/packages/core/volcano/generate-manifests.sh'\n"
    f"# Volcano v{version}\n---\n"
)
with open(dst, "w") as f:
    f.write(header + "\n---\n".join(yaml.safe_dump(d, sort_keys=False) for d in docs))
print(f"wrote {dst}: {len(docs)} objects")
PYEOF
