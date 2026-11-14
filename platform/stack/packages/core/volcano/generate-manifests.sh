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

# Narrow the controllers' and scheduler's ClusterRoles to what the v1.16.0 binaries
# call with the components and job plugins this package runs (2026-11-14, every
# removal checked against the v1.16.0 source tree and rehearsed against a real API
# server, TestVolcanoComponentsRunOnTheirNarrowedRBAC). What goes, and why it was
# dangerous rather than merely unused:
#   customresourcedefinitions create/delete   no CRD client exists anywhere in either
#                                             binary; it could rewrite any CRD on the cluster
#   secrets (controllers)                     only the `ssh` job plugin uses it; cluster-wide
#                                             `get` reads every credential on the platform.
#                                             The platform's runs use the `env` plugin only
#                                             (design 0025 §4.5); an ssh-plugin vcjob is refused
#                                             by the API server instead
#   leases                                    leader election is off (one replica); a
#                                             cluster-wide lease writer can take any
#                                             controller's leadership, kube-system's included
#   configmap/service writes,                 only the `svc` job plugin (and the tensorflow/
#   networkpolicies (controllers)             mpi/pytorch/ray plugins, which the job webhook
#                                             makes pull `svc` in) write them. Cluster-wide,
#                                             that is a ServiceAccount that can rewrite
#                                             volcano-scheduler-configmap, cilium-config,
#                                             coredns or argocd-cm: the very reason design
#                                             §4.1 leaves the dashboard out. The platform's
#                                             runs use `env` only ("no svc plugin", §4.5), so
#                                             such a vcjob is refused by the API server
#                                             instead (2026-11-14). The job controller keeps
#                                             its Service informer, so services stay readable
#   flow.volcano.sh                           the JobFlow CRDs are not installed
#   colocationconfigurations, nodeshards,     their controllers are disabled in values.yaml
#   hypernode writes (controllers)
#   priorityclasses create/delete             read through an informer only
#   nodes update/patch (scheduler)            only the deviceshare/vGPU node lock, not enabled
#   configmap writes (scheduler)              only the fairshare plugin persists state, not enabled
def narrow(role_name, edits, why):
    role = only("ClusterRole", role_name)
    for key, verbs in edits.items():
        group, resource = key
        hit = [
            r
            for r in role["rules"]
            if group in r.get("apiGroups", []) and resource in r.get("resources", [])
        ]
        if not hit:
            sys.exit(f"{role_name}: no rule grants {group or 'core'}/{resource} ({why}); re-read this edit")
        for r in hit:
            others = [x for x in r["resources"] if x != resource]
            if others:
                # Split the rule so the neighbours keep their verbs.
                role["rules"].append({**r, "resources": others})
                r["resources"] = [resource]
            if verbs:
                r["verbs"] = [v for v in r["verbs"] if v in verbs]
            else:
                r["verbs"] = []
    role["rules"] = [r for r in role["rules"] if r.get("verbs")]


READ = ("get", "list", "watch")
narrow(
    "volcano-controllers",
    {
        ("apiextensions.k8s.io", "customresourcedefinitions"): None,
        ("", "secrets"): None,
        ("", "configmaps"): READ,
        ("", "services"): READ,
        ("networking.k8s.io", "networkpolicies"): None,
        ("coordination.k8s.io", "leases"): None,
        ("flow.volcano.sh", "jobflows"): None,
        ("flow.volcano.sh", "jobtemplates"): None,
        ("flow.volcano.sh", "jobflows/status"): None,
        ("flow.volcano.sh", "jobs/finalizers"): None,
        ("flow.volcano.sh", "jobtemplates/status"): None,
        ("flow.volcano.sh", "jobtemplates/finalizers"): None,
        ("config.volcano.sh", "colocationconfigurations"): None,
        ("shard.volcano.sh", "nodeshards"): None,
        ("shard.volcano.sh", "nodeshards/status"): None,
        ("topology.volcano.sh", "hypernodes"): READ,
        ("topology.volcano.sh", "hypernodes/status"): None,
        ("scheduling.k8s.io", "priorityclasses"): READ,
    },
    "controllers",
)
narrow(
    "volcano-scheduler",
    {
        ("apiextensions.k8s.io", "customresourcedefinitions"): None,
        ("coordination.k8s.io", "leases"): None,
        ("", "configmaps"): READ,
        ("", "nodes"): READ,
    },
    "scheduler",
)

# Unbound and unaggregated upstream, so inert today, but they are the second copy of
# the user-facing Volcano grants. core/adhar-batch's adhar-batch-view/-edit/-operator
# (design 0025 §10.2) are the only ones, so a reviewer reads one set.
take(
    lambda d: d["kind"] == "ClusterRole" and d["metadata"]["name"] in ("vcjob-editor-role", "vcjob-viewer-role"),
    2,
    "vcjob editor/viewer roles",
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

# Flags whose built-in default is `volcano-system` (vc-scheduler/vc-controller-manager
# --help, v1.16.0). The chart writes them only while leader election or sharding is
# on, so with both off they fall back to upstream's namespace: unused today, and the
# first thing to break the day someone turns leader election back on (CONFLICTS.md
# "Namespace references hide in … CLI flags"). Pinned here so no flag of any
# component names a namespace other than adhar-system.
hidden_namespace_flags = {
    "volcano-scheduler": ["--leader-elect-resource-namespace"],
    "volcano-controllers": ["--leader-elect-resource-namespace", "--sharding-configmap-namespace"],
}
for name, flags in hidden_namespace_flags.items():
    args = deployments[name]["spec"]["template"]["spec"]["containers"][0]["args"]
    for flag in flags:
        if any(a.startswith(flag + "=") for a in args):
            sys.exit(f"{name}: the chart now renders {flag}; drop it from hidden_namespace_flags")
        # Before the trailing "2>&1" the chart appends to every component.
        args.insert(len(args) - 1 if args[-1] == "2>&1" else len(args), f"{flag}=adhar-system")

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
