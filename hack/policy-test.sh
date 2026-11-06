#!/usr/bin/env bash
# Prove the Kyverno policies the stack ships against their fixtures: every
# `kyverno-test.yaml` under platform/stack/packages (a package's `policy-tests/`)
# is run with the kyverno CLI the platform's admission controller matches.
#
#   make policy-test                 # downloads the pinned CLI into bin/ once
#   KYVERNO=/path/to/kyverno hack/policy-test.sh [dir ...]
#
# Why this wraps `kyverno test` instead of trusting its exit code: the CLI counts
# two kinds of assertion that never ran as PASSED —
#   * a policy Kyverno refuses to load ("Invalid Policy", e.g. a rule-level
#     message that reads a foreach `element`): every expected result is reported
#     as a skip and the run exits 0;
#   * an expected result for a rule or resource the policy never evaluated (a rule
#     name with a typo, a Pod-controller rule Kyverno did not generate) is
#     reported "Excluded" and counted as a pass.
# So every row must come back Pass with reason Ok, except an expected `skip`,
# which may legitimately be "Excluded" (an excluded subject reads exactly so).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LOCALBIN="${LOCALBIN:-${ROOT}/bin}"
# The admission controller the platform deploys (security/kyverno, chart 3.9.0).
KYVERNO_VERSION="${KYVERNO_VERSION:-v1.19.0}"
KYVERNO="${KYVERNO:-${LOCALBIN}/kyverno}"

install_kyverno() {
  local target="${LOCALBIN}/kyverno-${KYVERNO_VERSION}"
  if [ ! -x "${target}" ]; then
    local os arch asset url work
    os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    case "$(uname -m)" in
      arm64 | aarch64) arch=arm64 ;;
      x86_64 | amd64) arch=x86_64 ;;
      *) echo "policy-test: no kyverno CLI build for $(uname -m)" >&2; exit 1 ;;
    esac
    asset="kyverno-cli_${KYVERNO_VERSION}_${os}_${arch}.tar.gz"
    url="https://github.com/kyverno/kyverno/releases/download/${KYVERNO_VERSION}"
    work="$(mktemp -d)"
    trap 'rm -rf "${work}"' RETURN
    echo "policy-test: downloading kyverno CLI ${KYVERNO_VERSION} (${os}/${arch})"
    curl -fsSL -o "${work}/${asset}" "${url}/${asset}"
    curl -fsSL -o "${work}/checksums.txt" "${url}/checksums.txt"
    (cd "${work}" && grep " ${asset}\$" checksums.txt | shasum -a 256 -c - >/dev/null) || {
      echo "policy-test: ${asset} does not match the release checksums" >&2
      exit 1
    }
    tar -xzf "${work}/${asset}" -C "${work}" kyverno
    mkdir -p "${LOCALBIN}"
    mv "${work}/kyverno" "${target}"
    chmod +x "${target}"
  fi
  ln -sf "${target}" "${LOCALBIN}/kyverno"
}

if [ "${KYVERNO}" = "${LOCALBIN}/kyverno" ]; then
  install_kyverno
fi
# Not `| head -1`: under pipefail the CLI's SIGPIPE would end the script here.
version="$("${KYVERNO}" version 2>/dev/null || true)"
echo "${version%%$'\n'*}"

if [ "$#" -gt 0 ]; then
  dirs=("$@")
else
  dirs=()
  while IFS= read -r f; do dirs+=("$(dirname "${f}")"); done < <(
    find "${ROOT}/platform/stack/packages" -name kyverno-test.yaml -not -path '*/.git/*' | sort
  )
fi
if [ "${#dirs[@]}" -eq 0 ]; then
  echo "policy-test: no kyverno-test.yaml under platform/stack/packages" >&2
  exit 1
fi

# Checks one directory's CLI output against its kyverno-test.yaml (stdin: output).
read -r -d '' CHECK <<'PYEOF' || true
import json
import sys

import yaml

test_file, code, rel = sys.argv[1], int(sys.argv[2]), sys.argv[3]  # argv[0] is "-c"
out = sys.stdin.read()

# The expected rows, in the order the CLI reports them: one per resource of
# each result entry.
spec = yaml.safe_load(open(test_file))
expected = []
for r in spec.get("results") or []:
    for res in r.get("resources") or []:
        expected.append((r["policy"], r.get("rule", ""), res, r["result"], r.get("operation", "CREATE")))

start = out.find("\n[")
if start < 0:
    print(f"FAIL {rel}: kyverno test printed no results (exit {code})\n{out}")
    sys.exit(1)
rows, _ = json.JSONDecoder().raw_decode(out[start + 1:])

problems = []
if code != 0:
    problems.append(f"kyverno test exited {code}")
if len(rows) != len(expected):
    problems.append(f"{len(rows)} results for {len(expected)} expectations")
for row, (policy, rule, res, want, op) in zip(rows, expected):
    where = f"{policy}/{rule} {op} {res} (want {want})"
    if row.get("POLICY") != policy or row.get("RULE") != rule or not row.get("RESOURCE", "").endswith("/" + res):
        problems.append(f"cannot correlate {where} with {row.get('RESOURCE')} {row.get('RULE')}")
        continue
    reason = row.get("REASON")
    if row.get("RESULT") != "Pass":
        problems.append(f"{where}: {row.get('RESULT')} ({reason})")
    elif want in ("pass", "fail") and reason != "Ok":
        problems.append(f"{where}: never evaluated ({reason})")
    elif want == "skip" and reason not in ("Ok", "Excluded"):
        problems.append(f"{where}: {reason}")

if problems:
    print(f"FAIL {rel}")
    for p in problems:
        print(f"  - {p}")
    sys.exit(1)
print(f"ok   {rel} ({len(rows)} assertions)")
PYEOF

failed=0
for dir in "${dirs[@]}"; do
  rel="${dir#"${ROOT}/"}"
  set +e
  out="$("${KYVERNO}" test "${dir}" --remove-color -o json 2>&1)"
  code=$?
  set -e
  if ! python3 -c "${CHECK}" "${dir}/kyverno-test.yaml" "${code}" "${rel}" <<<"${out}"; then
    failed=1
  fi
done

if [ "${failed}" -ne 0 ]; then
  echo "policy-test: FAILED" >&2
  exit 1
fi
echo "policy-test: all policy tests passed"
