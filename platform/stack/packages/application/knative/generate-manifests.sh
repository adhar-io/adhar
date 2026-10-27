#!/bin/bash
set -e

# Knative is installed via the Knative Operator, which is distributed as a
# static release manifest (not a Helm chart). We pin to a Knative Operator
# release and vendor its operator.yaml. Knative Serving/Eventing CRs are then
# created declaratively to let the operator install the data plane.
#
# Networking: the Knative Operator itself exposes no ingress. When Serving is
# configured it uses its own networking layer (Kourier/Istio), NOT nginx.
#
# Placement (ADR-0011 exception, see packages/CONFLICTS.md): the OPERATOR runs in
# adhar-system like every other platform controller, but it installs Serving and
# Eventing into the namespace of their CRs, and those CRs live in knative-serving
# and knative-eventing (manifests/serving.yaml, eventing.yaml). Knative's data
# plane cannot share adhar-system: its webhook's leases and Secret/webhook-certs
# are compiled into kpack's webhook too, its config-* ConfigMaps are Tekton's
# names, and its Service/webhook injects WEBHOOK_PORT into every platform pod.
#
# Post-processing (all of it below, so operator.yaml is reproducible from
# upstream; the python step refuses to write a file that misses any of it):
#   1. knative-operator namespace -> adhar-system, and upstream's Namespace
#      object dropped (packages never ship the platform namespace).
#   2. The operator's own config-logging/config-observability ConfigMaps are
#      renamed knative-operator-config-*, and the CONFIG_*_NAME env vars of both
#      operator Deployments repointed. Tekton ships ConfigMaps with exactly those
#      names in adhar-system, so two Applications owned one object and flapped it
#      OutOfSync on every sync; renaming the object alone would silently make the
#      operator read Tekton's logging config.
#   3. `rules: []` is dropped from the aggregated ClusterRoles. The controller
#      manager fills an aggregated role's rules; an Application that also declares
#      them (as empty) is OutOfSync forever and resets them on every self-heal.
#      Kyverno's, kpack's and Crossplane's aggregated roles ship no rules either.
#   4. enableServiceLinks: false on both operator Deployments (the standing
#      ADR-0011 rule): adhar-system holds ~300 Services, and knative.dev/pkg reads
#      *_PORT-shaped variables such as WEBHOOK_PORT from the environment.

INSTALL_YAML="manifests/operator.yaml"
OPERATOR_VERSION="v1.23.1"

echo "# KNATIVE OPERATOR INSTALL RESOURCES" >${INSTALL_YAML}
echo "# This file is auto-generated with 'platform/stack/packages/application/knative/generate-manifests.sh'" >>${INSTALL_YAML}
echo "# Knative Operator ${OPERATOR_VERSION}" >>${INSTALL_YAML}

curl -sSfL "https://github.com/knative/operator/releases/download/knative-${OPERATOR_VERSION}/operator.yaml" >>${INSTALL_YAML}

# (1) The operator itself runs in adhar-system. Newer upstream operator bundles
# default to the knative-operator namespace and include a matching Namespace
# object; rewrite all namespace references and drop that extra namespace
# resource.
sed -i.bak 's/^\( *\)namespace: knative-operator$/\1namespace: adhar-system/' "${INSTALL_YAML}"
rm -f "${INSTALL_YAML}.bak"

awk '
function flush_doc() {
  if (doc == "") {
    return
  }
  if (doc ~ /\nkind: Namespace\n/ && doc ~ /\nmetadata:\n/ && doc ~ /\n  name: knative-operator\n/) {
    doc = ""
    return
  }
  printf "%s", doc
  doc = ""
}
{
  if ($0 == "---") {
    flush_doc()
    doc = "---\n"
    next
  }
  doc = doc $0 "\n"
}
END {
  flush_doc()
}
' "${INSTALL_YAML}" > "${INSTALL_YAML}.tmp"
mv "${INSTALL_YAML}.tmp" "${INSTALL_YAML}"

# (2)-(4) Text edits, so upstream's comments and layout survive and a version
# bump diffs cleanly, then a structural check of the result.
python3 - "${INSTALL_YAML}" <<'PYEOF'
import re, sys, yaml

path = sys.argv[1]
text = open(path).read()
RENAMED = {
    "config-logging": "knative-operator-config-logging",
    "config-observability": "knative-operator-config-observability",
}
ENV = {"CONFIG_LOGGING_NAME": "config-logging",
       "CONFIG_OBSERVABILITY_NAME": "config-observability"}

docs = re.split(r"(?m)^(?=---\n)", text)
renamed = stripped = unlinked = 0
for i, doc in enumerate(docs):
    if re.search(r"(?m)^kind: ConfigMap$", doc):
        for old, new in RENAMED.items():
            doc, n = re.subn(rf"(?m)^  name: {old}$", f"  name: {new}", doc)
            renamed += n
    if re.search(r"(?m)^kind: ClusterRole$", doc) and re.search(r"(?m)^aggregationRule:$", doc):
        doc, n = re.subn(r"(?m)^rules: \[\].*\n", "", doc)
        stripped += n
    if re.search(r"(?m)^kind: Deployment$", doc) and "enableServiceLinks" not in doc:
        doc, n = re.subn(r"(?m)^(      serviceAccountName: \S+\n)",
                         r"\1      enableServiceLinks: false\n", doc)
        unlinked += n
    docs[i] = doc
text = "".join(docs)

repointed = 0
for var, old in ENV.items():
    text, n = re.subn(rf"(- name: {var}\n\s+value: ){old}$", rf"\g<1>{RENAMED[old]}",
                      text, flags=re.M)
    repointed += n

objs = [d for d in yaml.safe_load_all(text) if d]
problems = []
configmaps = {d["metadata"]["name"] for d in objs if d["kind"] == "ConfigMap"}
if configmaps != set(RENAMED.values()):
    problems.append(f"operator ConfigMaps are {sorted(configmaps)}, want {sorted(RENAMED.values())}")
for d in objs:
    name = f'{d["kind"]}/{d["metadata"]["name"]}'
    if d["kind"] == "Namespace":
        problems.append(f"{name} survived; the package must not ship it")
    if d.get("metadata", {}).get("namespace") not in (None, "adhar-system"):
        problems.append(f"{name} is in {d['metadata']['namespace']}, not adhar-system")
    if d["kind"] == "ClusterRole" and "aggregationRule" in d and "rules" in d:
        problems.append(f"{name} is aggregated but still declares rules")
    if d["kind"] != "Deployment":
        continue
    pod = d["spec"]["template"]["spec"]
    if pod.get("enableServiceLinks") is not False:
        problems.append(f"{name} keeps service links on")
    for c in pod["containers"]:
        for e in c.get("env", []):
            if e["name"] in ENV and e.get("value") not in configmaps:
                problems.append(f"{name}: {e['name']}={e.get('value')} names no ConfigMap in this file")
if problems:
    sys.exit("operator.yaml post-processing failed:\n  " + "\n  ".join(problems))

open(path, "w").write(text)
print(f"renamed {renamed} ConfigMap(s), repointed {repointed} CONFIG_*_NAME env var(s), "
      f"dropped rules from {stripped} aggregated ClusterRole(s), "
      f"disabled service links on {unlinked} Deployment(s)")
PYEOF
