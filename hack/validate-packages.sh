#!/usr/bin/env bash
#
# validate-packages.sh — validate every package's marketplace contract.
#
# Finds all `adhar-package.yaml` files under platform/stack/packages/ and
# validates each against platform/stack/packages/marketplace.schema.json
# (JSON Schema draft-07). Also enforces two cross-file invariants that the
# schema alone cannot express:
#   * the package `name` matches its directory name
#   * the package `category` matches its top-level directory
#   * `planeAffinity: control-plane` is not contradicted by a DaemonSet in the
#     package's own manifests (a per-node agent is data-plane, or any)
#   * every `dependencies[].name` resolves to a real package or a bootstrap
#     component, and no package depends on itself
#   * `stability: stable` is only claimed by packages a curated ApplicationSet
#     actually enables (MARKETPLACE.md section 3)
#
# Exit codes:
#   0  all found contracts are valid
#   1  one or more contracts failed validation
#   2  tooling missing (no validator available)
#
# Validators, in order of preference:
#   1. python3 with the `jsonschema` module (pip3 install --user jsonschema pyyaml)
#   2. `check-jsonschema` CLI (pipx install check-jsonschema) as a fallback
#
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PKG_DIR="${REPO_ROOT}/platform/stack/packages"
SCHEMA="${PKG_DIR}/marketplace.schema.json"

if [[ ! -f "${SCHEMA}" ]]; then
  echo "ERROR: schema not found at ${SCHEMA}" >&2
  exit 2
fi

# Collect contract files (portable; no mapfile dependency).
FILES=()
while IFS= read -r f; do
  FILES+=("$f")
done < <(find "${PKG_DIR}" -type f -name 'adhar-package.yaml' | sort)

if [[ ${#FILES[@]} -eq 0 ]]; then
  echo "No adhar-package.yaml files found under ${PKG_DIR} — nothing to validate."
  exit 0
fi

echo "Found ${#FILES[@]} package contract(s) to validate against:"
echo "  ${SCHEMA}"
echo

# ---------------------------------------------------------------------------
# Preferred path: python3 + jsonschema (handles YAML parsing + name/category
# cross-checks in one pass and reports every failure).
# ---------------------------------------------------------------------------
if python3 -c 'import jsonschema, yaml' >/dev/null 2>&1; then
  PKG_DIR="${PKG_DIR}" SCHEMA="${SCHEMA}" python3 - "${FILES[@]}" <<'PY'
import json
import os
import re
import sys

import yaml
from jsonschema import Draft7Validator

pkg_dir = os.environ["PKG_DIR"]
schema_path = os.environ["SCHEMA"]

with open(schema_path) as fh:
    schema = json.load(fh)

# Fail fast if the schema itself is not a valid draft-07 document.
Draft7Validator.check_schema(schema)
validator = Draft7Validator(schema)

_DAEMONSET_RE = re.compile(r"^\s*kind:\s*DaemonSet\s*$", re.MULTILINE)


def _daemonset_manifest(pkg_root):
    """Return the first manifest under pkg_root declaring a DaemonSet, else None.

    Charts pulled at sync time are invisible here, so this can only
    under-detect — it never fails a package for a DaemonSet it does not ship.
    """
    for root, _, names in os.walk(pkg_root):
        for name in sorted(names):
            if name == "adhar-package.yaml":
                continue
            if not name.endswith((".yaml", ".yml", ".tmpl")):
                continue
            full = os.path.join(root, name)
            try:
                with open(full, encoding="utf-8", errors="replace") as fh:
                    if _DAEMONSET_RE.search(fh.read()):
                        return os.path.relpath(full, pkg_root)
            except OSError:
                continue
    return None


# Curated-set enablement, read straight from the ApplicationSets. MARKETPLACE.md
# §3 defines `stable` as "enabled in curated sets", so a package no profile
# enables cannot claim it. Read here rather than trusted, because the first
# scaffolded pass derived stability from the package's DIRECTORY — which made
# `trivy` stable while no profile enabled it.
def _curated_enablement():
    stack_dir = os.path.dirname(pkg_dir)
    enabled = {}
    found_any = False
    for fname in ("adhar-appset-local.yaml", "adhar-appset-production.yaml"):
        path = os.path.join(stack_dir, fname)
        if not os.path.exists(path):
            continue
        try:
            with open(path) as fh:
                doc = yaml.safe_load(fh)
        except yaml.YAMLError:
            continue
        found_any = True
        for gen in (doc.get("spec", {}) or {}).get("generators", []) or []:
            for el in ((gen.get("list") or {}).get("elements") or []):
                parts = str(el.get("manifestPath", "")).split("/")
                if len(parts) >= 2 and str(el.get("enabled", "false")) == "true":
                    enabled["/".join(parts[:2])] = True
    # No appsets found (a partial checkout): skip the check rather than fail
    # every package for a file that is not there.
    return enabled if found_any else None


CURATED_ENABLED = _curated_enablement()

# Bootstrap components are installed imperatively by the AdharPlatform
# controller, not as stack packages, so they have no package directory — but a
# dependency on them is real (Kargo genuinely needs ArgoCD). Accept them by name
# so the resolution check below can be strict about everything else.
BOOTSTRAP_COMPONENTS = {"argocd", "gitea", "cilium", "gateway", "crossplane"}

# Every package directory name, lowercased: the universe a dependency may name.
KNOWN_PACKAGES = {
    os.path.basename(os.path.dirname(p)).lower() for p in sys.argv[1:]
} | BOOTSTRAP_COMPONENTS

failures = 0
for path in sys.argv[1:]:
    rel = os.path.relpath(path, pkg_dir)
    try:
        with open(path) as fh:
            doc = yaml.safe_load(fh)
    except yaml.YAMLError as exc:
        print(f"FAIL  {rel}\n    - YAML parse error: {exc}")
        failures += 1
        continue

    errors = sorted(validator.iter_errors(doc), key=lambda e: list(e.path))

    # Cross-file invariants (directory layout is the source of truth).
    parts = rel.split(os.sep)
    if len(parts) >= 3 and isinstance(doc, dict):
        dir_category, dir_name = parts[0], parts[1]
        # Package names are lowercase DNS labels (schema `pattern`), while one
        # legacy directory is capitalised (core/Kamaji -> package `kamaji`, the
        # name the ApplicationSets already use). Compare case-insensitively so the
        # filesystem stays the source of truth without forcing an invalid
        # uppercase `name` into the contract.
        if (doc.get("name") or "").lower() != dir_name.lower():
            errors.append(type("E", (), {
                "message": f"name '{doc.get('name')}' does not match directory '{dir_name}'",
                "path": ["name"]})())
        if doc.get("category") != dir_category:
            errors.append(type("E", (), {
                "message": f"category '{doc.get('category')}' does not match directory '{dir_category}'",
                "path": ["category"]})())

        # planeAffinity must not contradict the manifests. A package that ships
        # a DaemonSet puts a pod on every node, workload nodes included, so it
        # cannot honestly claim `control-plane`: it is either a per-node agent
        # (`data-plane`) or a control component with a node-agent half (`any`).
        # This is the invariant the scaffolded contracts got wrong — the
        # generator defaulted every package to control-plane, so falco,
        # tetragon, beyla and pixie all claimed to be control-plane services.
        # Checked against the tree rather than trusted, because nothing consumes
        # planeAffinity yet and unread metadata rots silently.
        if doc.get("planeAffinity") == "control-plane":
            ds = _daemonset_manifest(os.path.join(pkg_dir, dir_category, dir_name))
            if ds:
                errors.append(type("E", (), {
                    "message": (
                        f"declares control-plane but ships a DaemonSet ({ds}); "
                        "use 'data-plane' if the package IS the node agent, "
                        "'any' if it also has control components"),
                    "path": ["planeAffinity"]})())

        # Dependencies must resolve. Nothing installs from these contracts yet,
        # so an unresolvable name would sit unnoticed until the marketplace
        # tried to act on it — and a package renamed or dropped elsewhere in the
        # tree leaves exactly that kind of dangling reference behind.
        for i, dep in enumerate(doc.get("dependencies") or []):
            if not isinstance(dep, dict):
                continue
            dep_name = (dep.get("name") or "").lower()
            if dep_name and dep_name not in KNOWN_PACKAGES:
                errors.append(type("E", (), {
                    "message": (
                        f"dependency '{dep.get('name')}' is neither a package "
                        "under platform/stack/packages/ nor a bootstrap "
                        f"component ({', '.join(sorted(BOOTSTRAP_COMPONENTS))})"),
                    "path": ["dependencies", i, "name"]})())

        # `stable` is a claim about exercise, not about the upstream project's
        # maturity: it means a curated profile actually runs this package, so
        # every platform bring-up is a regression test for it. A package no
        # profile enables has never been exercised by default and cannot make
        # that claim. Only this direction is checked — a maintainer may always
        # downgrade an enabled package they have lost confidence in.
        if doc.get("stability") == "stable" and CURATED_ENABLED is not None:
            if f"{dir_category}/{dir_name}" not in CURATED_ENABLED:
                errors.append(type("E", (), {
                    "message": (
                        "claims 'stable' but is enabled in neither "
                        "adhar-appset-local.yaml nor adhar-appset-production.yaml; "
                        "MARKETPLACE.md \u00a73 defines stable as enabled in the "
                        "curated sets (use 'beta' or 'alpha')"),
                    "path": ["stability"]})())
            if dep_name and dep_name == (doc.get("name") or "").lower():
                errors.append(type("E", (), {
                    "message": "package depends on itself",
                    "path": ["dependencies", i, "name"]})())

    if errors:
        print(f"FAIL  {rel}")
        for err in errors:
            loc = "/".join(str(p) for p in getattr(err, "path", [])) or "<root>"
            print(f"    - {loc}: {err.message}")
        failures += 1
    else:
        print(f"OK    {rel}")

print()
if failures:
    print(f"{failures} package contract(s) FAILED validation.")
    sys.exit(1)
print("All package contracts are valid.")
PY
  exit $?
fi

# ---------------------------------------------------------------------------
# Fallback path: check-jsonschema CLI. It consumes YAML directly but cannot do
# the name/category cross-checks, so those are skipped with a warning.
# ---------------------------------------------------------------------------
if command -v check-jsonschema >/dev/null 2>&1; then
  echo "NOTE: using check-jsonschema fallback; name/category directory cross-checks are skipped." >&2
  # --schemafile validates every file; check-jsonschema exits non-zero on any failure.
  check-jsonschema --schemafile "${SCHEMA}" "${FILES[@]}"
  exit $?
fi

echo "ERROR: no JSON Schema validator available." >&2
echo "Install one of:" >&2
echo "  pip3 install --user jsonschema pyyaml   # preferred" >&2
echo "  pipx install check-jsonschema        # fallback" >&2
exit 2
