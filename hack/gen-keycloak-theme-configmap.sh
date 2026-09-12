#!/usr/bin/env bash
#
# gen-keycloak-theme-configmap.sh — build the Keycloak theme ConfigMap from the
# theme directory, which is the source of truth.
#
# The theme exists twice in the tree: as real files under
# platform/stack/packages/security/keycloak/theme/adhar/login/ (readable, and what
# a developer edits) and embedded in manifests/theme-configmap.yaml (what actually
# deploys, because ConfigMap keys cannot contain '/'). Keeping those in step by
# hand has already failed once: theme.properties drifted to `parent=keycloak`
# while the deployed ConfigMap carried the correct `parent=keycloak.v2`, which
# broke the login page's base styling. This script removes the whole class of
# problem — edit the files, run this, commit both.
#
# Usage:
#   hack/gen-keycloak-theme-configmap.sh            # write the ConfigMap
#   hack/gen-keycloak-theme-configmap.sh --check    # verify it is up to date (CI)
#
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PKG="${REPO_ROOT}/platform/stack/packages/security/keycloak"
THEME="${PKG}/theme/adhar/login"
OUT="${PKG}/manifests/theme-configmap.yaml"

MODE="write"
[[ "${1:-}" == "--check" ]] && MODE="check"

[[ -d "${THEME}" ]] || { echo "ERROR: theme directory not found at ${THEME}" >&2; exit 2; }

# ConfigMap key -> path inside the theme directory. The key is flat (no '/'); the
# nested layout is restored by items[].path in the Deployment volume, which
# install.yaml.tmpl must list for every key below.
FILES=(
  "theme.properties:theme.properties"
  "adhar.css:resources/css/adhar.css"
  "adhar-theme.js:resources/js/adhar-theme.js"
  "adhar-symbol.svg:resources/img/adhar-symbol.svg"
  "adhar-logo.svg:resources/img/adhar-logo.svg"
)

# Structural check on the stylesheet before it is embedded.
#
# CSS comments DO NOT NEST: `/* a /* b */ c */` ends at the FIRST `*/`, and
# everything after it is parsed as CSS. A nested `/* … */` inside one of this
# theme's long explanatory comments therefore turns the remaining prose into a
# selector, which silently swallows the rule that follows it — the container
# geometry fix was dropped by the browser exactly this way while looking correct
# in the file. Neither ArgoCD nor Keycloak will complain, so check it here.
check_css() {
  python3 - "$1" <<'PYEOF'
import re, sys

path = sys.argv[1]
raw = open(path, encoding="utf-8").read()
problems = []

# 1. No nested comment openers — walk comments the way a parser does.
i = 0
while True:
    a = raw.find("/*", i)
    if a < 0:
        break
    b = raw.find("*/", a + 2)
    if b < 0:
        problems.append(f"unterminated comment opened at line {raw[:a].count(chr(10)) + 1}")
        break
    body = raw[a + 2:b]
    if "/*" in body:
        outer = raw[:a].count(chr(10)) + 1
        inner = raw[:a + 2 + body.find("/*")].count(chr(10)) + 1
        problems.append(
            f"comment opened at line {outer} contains a nested '/*' at line {inner}; "
            "CSS comments do not nest, so the comment ends early and the rest is parsed as CSS")
    i = b + 2

# 2. Braces must balance once comments are blanked (newlines preserved so the
#    reported line numbers match the real file).
blanked = re.sub(r"/\*.*?\*/", lambda m: re.sub(r"[^\n]", " ", m.group(0)), raw, flags=re.S)
depth = 0
for lineno, line in enumerate(blanked.split("\n"), 1):
    for ch in line:
        if ch == "{":
            depth += 1
        elif ch == "}":
            depth -= 1
            if depth < 0:
                problems.append(f"stray '}}' at line {lineno}")
                depth = 0
if depth:
    problems.append(f"{depth} unclosed block(s) at end of file")

if problems:
    print(f"ERROR: {path} is structurally invalid:", file=sys.stderr)
    for p in problems:
        print(f"  - {p}", file=sys.stderr)
    sys.exit(1)
PYEOF
}

check_css "${THEME}/resources/css/adhar.css"

TMP="$(mktemp)"
trap 'rm -f "${TMP}"' EXIT

cat > "${TMP}" <<'HEADER'
# Adhar-branded Keycloak login theme, mounted into the Keycloak pod at
# /opt/keycloak/themes/adhar via the volume in install.yaml. The realm's
# loginTheme is set to `adhar` (keycloak-config.yaml).
#
# GENERATED — do not edit by hand.
#   source:    ../theme/adhar/login/
#   generator: hack/gen-keycloak-theme-configmap.sh
# Edit the files under theme/adhar/login/ and re-run the generator.
apiVersion: v1
kind: ConfigMap
metadata:
  name: keycloak-theme-adhar
  namespace: adhar-system
  labels:
    app: keycloak
    adhar.io/package-name: keycloak
data:
HEADER

for entry in "${FILES[@]}"; do
  key="${entry%%:*}"
  rel="${entry#*:}"
  src="${THEME}/${rel}"
  [[ -f "${src}" ]] || { echo "ERROR: missing theme file ${src}" >&2; exit 2; }
  printf '  %s: |\n' "${key}" >> "${TMP}"
  # Four-space indent under the block scalar. `sed` leaves blank lines empty
  # rather than indenting them, which keeps the YAML clean and is valid for a
  # literal block scalar.
  sed -e 's/^\(.\)/    \1/' "${src}" >> "${TMP}"
done

if [[ "${MODE}" == "check" ]]; then
  if diff -u "${OUT}" "${TMP}" > /dev/null 2>&1; then
    echo "OK  ${OUT#"${REPO_ROOT}/"} is up to date with theme/adhar/login/"
    exit 0
  fi
  echo "FAIL ${OUT#"${REPO_ROOT}/"} is out of date. Run hack/gen-keycloak-theme-configmap.sh" >&2
  diff -u "${OUT}" "${TMP}" | head -40 >&2
  exit 1
fi

mv "${TMP}" "${OUT}"
trap - EXIT
echo "wrote ${OUT#"${REPO_ROOT}/"} from ${#FILES[@]} theme file(s)"
