#!/bin/bash
set -e

# Karmada is published as a GitHub release asset, not from a Helm repository:
# https://karmada-io.github.io/charts returns 404. The chart tarball ships with
# every release, so the version here is the Karmada version.
INSTALL_YAML="manifests/install.yaml"
CHART_VERSION="1.19.0"
CHART_URL="https://github.com/karmada-io/karmada/releases/download/v${CHART_VERSION}/karmada-chart-v${CHART_VERSION}.tgz"

echo "# KARMADA (multi-cluster control plane) INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/core/karmada/generate-manifests.sh'" >>${INSTALL_YAML}
echo "# Karmada v${CHART_VERSION}" >>${INSTALL_YAML}

TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
curl -fsSL -o "${TMP}/karmada-chart.tgz" "${CHART_URL}"

helm template --namespace adhar-system karmada "${TMP}/karmada-chart.tgz" \
  -f values.yaml --version "${CHART_VERSION}" --include-crds >>${INSTALL_YAML}

# Translate the chart's Helm hooks into Argo CD hooks, and make the one Job that
# has no hook at all into one.
#
# This matters because of how a Job behaves under GitOps. Argo CD does not read
# `helm.sh/hook` — `helm template` leaves those annotations as inert text — so
# every one of these Jobs is managed as an ordinary resource. The Job controller
# then stamps `spec.selector` and the `controller-uid` labels onto the live
# object, git has neither, and that difference never goes away. Argo CD sees
# OutOfSync, tries to sync, finds a Job spec is immutable, and so deletes and
# recreates it — forever. Observed on this cluster as `karmada-static-resource`
# being recreated every ~50 seconds with the Application flapping
# Progressing → Healthy → Progressing and never settling.
#
# A one-shot bootstrap Job IS a hook, so saying so fixes it properly: Argo CD
# runs it, does not diff it, and `BeforeHookCreation` clears the previous run
# instead of colliding with its immutable spec.
python3 - "${INSTALL_YAML}" <<'PYEOF'
import sys, yaml

path = sys.argv[1]
docs = [d for d in yaml.safe_load_all(open(path)) if d]

# Helm's lifecycle phases, mapped onto Argo CD's.
PHASE = {
    'pre-install': 'PreSync',
    'pre-upgrade': 'PreSync',
    'post-install': 'PostSync',
    'post-upgrade': 'PostSync',
    'post-delete': 'PostDelete',
}

changed = []
for d in docs:
    if d.get('kind') != 'Job':
        continue
    meta = d.setdefault('metadata', {})
    ann = meta.setdefault('annotations', {})
    helm_hook = (ann.get('helm.sh/hook') or '').split(',')[0].strip()
    hook = PHASE.get(helm_hook, 'Sync')
    ann['argocd.argoproj.io/hook'] = hook
    # BeforeHookCreation, not HookSucceeded: a succeeded Job that is deleted
    # immediately loses the record of whether the bootstrap ran, and deleting it
    # before the NEXT run is what actually avoids the immutable-spec collision.
    ann['argocd.argoproj.io/hook-delete-policy'] = 'BeforeHookCreation'
    changed.append(f"{meta.get('name')} -> {hook}")

with open(path, 'w') as f:
    f.write('\n---\n'.join(yaml.safe_dump(d, sort_keys=False) for d in docs))
print('argo hooks set on %d Job(s): %s' % (len(changed), ', '.join(changed)))
PYEOF
