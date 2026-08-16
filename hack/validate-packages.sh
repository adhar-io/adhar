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
#
# Exit codes:
#   0  all found contracts are valid
#   1  one or more contracts failed validation
#   2  tooling missing (no validator available)
#
# Validators, in order of preference:
#   1. python3 with the `jsonschema` module (pip install jsonschema)
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
        if doc.get("name") != dir_name:
            errors.append(type("E", (), {
                "message": f"name '{doc.get('name')}' does not match directory '{dir_name}'",
                "path": ["name"]})())
        if doc.get("category") != dir_category:
            errors.append(type("E", (), {
                "message": f"category '{doc.get('category')}' does not match directory '{dir_category}'",
                "path": ["category"]})())

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
echo "  pip install jsonschema pyyaml        # preferred" >&2
echo "  pipx install check-jsonschema        # fallback" >&2
exit 2
