#!/bin/bash
set -e

INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="89.2.2"

echo "# KUBE-PROMETHEUS INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/observability/kube-prometheus/generate-manifests.sh'" >>${INSTALL_YAML}


helm repo add prometheus-community https://prometheus-community.github.io/helm-charts --force-update
helm repo update prometheus-community
helm template --include-crds --namespace adhar-system prometheus prometheus-community/kube-prometheus-stack -f values.yaml --version ${CHART_VERSION} --set crds.enabled=true >> ${INSTALL_YAML}
# Post-render fixes. These used to be applied BY HAND to install.yaml after
# templating and were not recorded here, so the first honest regeneration
# silently reverted both — and the regression only surfaced because a stack
# invariant test caught the probe. Anything that must survive a regen lives in
# this script, not in the rendered file.
python3 - "${INSTALL_YAML}" <<'PYEOF'
import sys, yaml

path = sys.argv[1]

# The chart embeds Grafana dashboards whose JSON contains a bare `=`, which
# PyYAML resolves to its `value` tag and refuses to construct. Load it as text.
class Loader(yaml.SafeLoader):
    pass
Loader.add_constructor('tag:yaml.org,2002:value', lambda l, n: l.construct_scalar(n))

docs = [d for d in yaml.load_all(open(path), Loader=Loader) if d]

waved = probes = 0
for d in docs:
    kind = d.get("kind")
    meta = d.setdefault("metadata", {})

    # 1. Monitoring CRs at wave 10. PrometheusRule and ServiceMonitor are
    #    instances of the operator's CRDs; on a fresh cluster they must not be
    #    applied before the operator exists (SkipDryRun cannot save a CR whose
    #    kind the apiserver has never heard of at sync time). Wave 10 places
    #    them after every foundation wave — the convention every other package
    #    on this platform follows for monitoring resources.
    if kind in ("PrometheusRule", "ServiceMonitor"):
        ann = meta.setdefault("annotations", {})
        if ann.get("argocd.argoproj.io/sync-wave") != "10":
            ann["argocd.argoproj.io/sync-wave"] = "10"
            waved += 1

    # 2. No probe may ship with timeoutSeconds: 1 (TestNoProbeShipsAOneSecondTimeout).
    #    The node-exporter subchart ships exactly that, and one second is less
    #    than a loaded node needs to answer, so kubelet kills a healthy exporter.
    if kind in ("Deployment", "StatefulSet", "DaemonSet"):
        for c in d.get("spec", {}).get("template", {}).get("spec", {}).get("containers", []):
            for probe in ("livenessProbe", "readinessProbe", "startupProbe"):
                p = c.get(probe)
                if isinstance(p, dict) and p.get("timeoutSeconds", 5) < 5:
                    p["timeoutSeconds"] = 5
                    probes += 1

with open(path, "w") as f:
    f.write("\n---\n".join(yaml.dump(d, sort_keys=False, Dumper=yaml.SafeDumper) for d in docs))
print(f"post-render: sync-wave 10 on {waved} monitoring CR(s), {probes} probe timeout(s) raised to 5s")
PYEOF
