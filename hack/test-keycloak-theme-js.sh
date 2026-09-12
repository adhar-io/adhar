#!/usr/bin/env bash
#
# test-keycloak-theme-js.sh — exercise the login theme's theme-resolution script.
#
# adhar-theme.js decides whether the Keycloak login page renders light or dark.
# Getting it wrong is a visible regression that no Go test can catch, and the
# interesting cases are the combinations: an explicit LIGHT choice on a DARK OS
# has to strip Keycloak's own `pf-v5-theme-dark` class, or PatternFly's inherited
# component styles stay dark underneath our light surfaces.
#
# The harness runs the real script in a `vm` context against a stub DOM, so there
# is no browser, no package.json and no dependency beyond Node core.
#
# Skips (exit 0) when node is unavailable, so it is safe to wire into a pipeline
# that does not provision Node.
#
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
THEME="${REPO_ROOT}/platform/stack/packages/security/keycloak/theme"
SCRIPT="${THEME}/adhar/login/resources/js/adhar-theme.js"
HARNESS="${THEME}/test/adhar-theme.test.js"

if ! command -v node >/dev/null 2>&1; then
  echo "SKIP: node not found on PATH — cannot run the login theme JS tests." >&2
  exit 0
fi

[[ -f "${SCRIPT}"  ]] || { echo "ERROR: missing ${SCRIPT}" >&2; exit 2; }
[[ -f "${HARNESS}" ]] || { echo "ERROR: missing ${HARNESS}" >&2; exit 2; }

node --check "${SCRIPT}"
exec node "${HARNESS}" "${SCRIPT}"
