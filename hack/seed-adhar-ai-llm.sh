#!/usr/bin/env bash
#
# seed-adhar-ai-llm.sh — give the platform its LLM key, turning Adhar AI from
# "installed" into "agentic".
#
# WHY A SCRIPT AND NOT A CONFIG FIELD. The key is a credential, and Adhar's rule
# is that git carries pointers and the secrets backend carries values (ADR-0009).
# Putting it in config.yaml would put it in git. So `adhar up` never takes the
# key: it installs Adhar AI unkeyed (the MCP servers and the agent runtime come
# up, the two backend-sourced ExternalSecrets report Degraded, nothing else is
# affected), and this script writes the value into OpenBao afterwards. ESO picks
# it up on its next refresh — no redeploy, no restart.
#
# The key is read from the environment and never echoed, never written to a file,
# and never passed on a command line that would land in shell history.
#
# Usage:
#   export ADHAR_AI_LLM_API_KEY='sk-...'
#   hack/seed-adhar-ai-llm.sh                       # Anthropic (platform default)
#   ADHAR_AI_LLM_PROVIDER=openai       hack/seed-adhar-ai-llm.sh
#   ADHAR_AI_LLM_PROVIDER=openrouter   hack/seed-adhar-ai-llm.sh
#
# Environment:
#   ADHAR_AI_LLM_API_KEY   (required) the provider API key
#   ADHAR_AI_LLM_PROVIDER  anthropic | openai | openrouter | openai-compatible
#                          (default: anthropic)
#   ADHAR_AI_LLM_MODEL     model id; a sensible per-provider default is used
#   ADHAR_AI_LLM_ENDPOINT  base URL, for openai-compatible / self-hosted
#   KUBECONFIG             the platform cluster
#
set -euo pipefail

: "${ADHAR_AI_LLM_API_KEY:?set ADHAR_AI_LLM_API_KEY to your provider API key}"
PROVIDER="${ADHAR_AI_LLM_PROVIDER:-anthropic}"
NS="adhar-system"

# Which Secret KEY the provider's agentgateway backend reads. `llm-anthropic`
# binds to API_KEY, `llm-openai` to OPENAI_API_KEY (llm-routes.yaml) — so the key
# has to land in the right one or the backend stays unkeyed and 401s.
case "${PROVIDER}" in
  anthropic|claude)
    MODEL="${ADHAR_AI_LLM_MODEL:-claude-opus-4-5-20251101}"
    ENDPOINT="${ADHAR_AI_LLM_ENDPOINT:-}"
    KV_PROVIDER="claude"
    SET_ANTHROPIC=1; SET_OPENAI=0 ;;
  openai)
    MODEL="${ADHAR_AI_LLM_MODEL:-gpt-4o}"
    ENDPOINT="${ADHAR_AI_LLM_ENDPOINT:-}"
    KV_PROVIDER="openai"
    SET_ANTHROPIC=0; SET_OPENAI=1 ;;
  openrouter)
    # OpenRouter is OpenAI-compatible, so it uses the OpenAI backend and its key
    # slot. Model ids are `<vendor>/<model>`, and only ids beginning `openai/`
    # (or gpt-/o1-…) match the HTTPRoute that selects that backend.
    MODEL="${ADHAR_AI_LLM_MODEL:-openai/gpt-4o-mini}"
    ENDPOINT="${ADHAR_AI_LLM_ENDPOINT:-https://openrouter.ai/api/v1}"
    KV_PROVIDER="openai"
    SET_ANTHROPIC=0; SET_OPENAI=1 ;;
  openai-compatible)
    : "${ADHAR_AI_LLM_ENDPOINT:?openai-compatible needs ADHAR_AI_LLM_ENDPOINT}"
    MODEL="${ADHAR_AI_LLM_MODEL:?openai-compatible needs ADHAR_AI_LLM_MODEL}"
    ENDPOINT="${ADHAR_AI_LLM_ENDPOINT}"
    KV_PROVIDER="openai-compatible"
    SET_ANTHROPIC=0; SET_OPENAI=1 ;;
  *)
    echo "ERROR: unknown ADHAR_AI_LLM_PROVIDER '${PROVIDER}'" >&2
    echo "       expected: anthropic | openai | openrouter | openai-compatible" >&2
    exit 2 ;;
esac

command -v kubectl >/dev/null 2>&1 || { echo "ERROR: kubectl not on PATH" >&2; exit 2; }
kubectl -n "${NS}" get pod openbao-0 >/dev/null 2>&1 \
  || { echo "ERROR: openbao-0 not found in ${NS} — is the secrets backend installed?" >&2; exit 2; }

# The backend root token lives in the openbao-keys Secret the bootstrap wrote.
TOKEN="$(kubectl -n "${NS}" get secret openbao-keys \
  -o jsonpath='{.data.init\.json}' 2>/dev/null | base64 -d \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["root_token"])')"
[[ -n "${TOKEN}" ]] || { echo "ERROR: could not read the OpenBao root token" >&2; exit 2; }

echo "Seeding secret/adhar-ai/llm  (provider=${KV_PROVIDER}, model=${MODEL})"

# `kv patch` so the OTHER properties (budgets, the provider key we are not
# setting) keep whatever they already hold.
#
# The KEY and the backend TOKEN travel on STDIN, one per line — `kubectl exec`
# has no --env, and putting them in argv would expose both in the container's
# process list. Everything else is non-secret and can be an argument.
printf '%s\n%s\n' "${TOKEN}" "${ADHAR_AI_LLM_API_KEY}" \
| kubectl exec -i -n "${NS}" openbao-0 -- env \
    BAO_ADDR=http://127.0.0.1:8200 \
    P="${KV_PROVIDER}" M="${MODEL}" E="${ENDPOINT}" \
    SET_ANTHROPIC="${SET_ANTHROPIC}" SET_OPENAI="${SET_OPENAI}" \
  sh -c '
    set -eu
    IFS= read -r BAO_TOKEN
    IFS= read -r K
    export BAO_TOKEN
    # Create the path on first run; patch it afterwards.
    if bao kv get secret/adhar-ai/llm >/dev/null 2>&1; then OP=patch; else OP=put; fi
    set -- "PROVIDER=$P" "MODEL=$M" "ENDPOINT=$E"
    if [ "$SET_ANTHROPIC" = "1" ]; then set -- "$@" "API_KEY=$K" "ANTHROPIC_API_KEY=$K"; fi
    if [ "$SET_OPENAI" = "1" ];    then set -- "$@" "OPENAI_API_KEY=$K"; fi
    if [ "$OP" = "put" ]; then
      set -- "$@" "BUDGET_PER_USER_DAILY_TOKENS=2000000" "BUDGET_PER_OP_MAX_TOOL_CALLS=40"
      if [ "$SET_ANTHROPIC" != "1" ]; then set -- "$@" "API_KEY=" "ANTHROPIC_API_KEY="; fi
      if [ "$SET_OPENAI" != "1" ];    then set -- "$@" "OPENAI_API_KEY="; fi
    fi
    bao kv "$OP" secret/adhar-ai/llm "$@" >/dev/null
    echo "  wrote secret/adhar-ai/llm ($OP)"
  '

# Nudge ESO rather than waiting out the 1h refreshInterval.
kubectl -n "${NS}" annotate externalsecret adhar-ai-llm \
  "force-sync=$(date +%s)" --overwrite >/dev/null 2>&1 || true

echo "Waiting for the adhar-ai-llm Secret to be projected..."
for _ in $(seq 1 20); do
  if [[ "$(kubectl -n "${NS}" get externalsecret adhar-ai-llm \
        -o jsonpath='{.status.conditions[0].status}' 2>/dev/null)" == "True" ]]; then
    echo "  ExternalSecret Ready — Adhar AI is keyed."
    echo
    echo "Next:"
    echo "  • the agent answers on  https://agent.<your-domain>"
    echo "  • completions route by model NAME through agentgateway at ai.<your-domain>/v1"
    echo "  • every request needs a platform token:  adhar auth token"
    if [[ "${KV_PROVIDER}" != "claude" && -n "${ENDPOINT}" ]]; then
      echo
      echo "NOTE for a non-default endpoint (${ENDPOINT}):"
      echo "  agentgateway's openai backend defaults to api.openai.com. Point it at"
      echo "  your endpoint and give it upstream TLS, or requests leave as plain HTTP:"
      echo "    docs/DIGITALOCEAN_PROVIDER.md  →  'Agentic platform (Adhar AI)'"
    fi
    exit 0
  fi
  sleep 6
done

echo "WARNING: the ExternalSecret did not report Ready in ~2 minutes." >&2
echo "  kubectl -n ${NS} describe externalsecret adhar-ai-llm" >&2
exit 1
