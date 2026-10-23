"""AI folder: one dashboard for the AI plane (the vLLM board stays with its package,
which is opt-in in every profile and ships it only when enabled).

  AI / Gateway & Runtime   the agent runtime (runs, models, tokens, MCP tools,
                           circuit breakers), the agentgateway data plane (traffic,
                           LLM cost / latency per model, guardrails, MCP federation)
                           and k8sgpt. Keeps the uid adhar-ai the platform page links to.

Its two halves are Adhar-authored and were verified against a live Prometheus when
they were written (see the notes in the JSON); they are kept as vendored sources in
dashboards-custom/vendored/ (formerly ai/adhar-ai/manifests/dashboard.yaml and
ai/agentgateway/manifests/dashboard.yaml) and folded into one dashboard here. Edit
the JSON, then re-run build-service-dashboards.py.
"""
import json
import os

from dashlib import *  # noqa: F401,F403
from dash_common import UIDS, links, tags

VENDORED = os.path.join(os.path.dirname(os.path.abspath(__file__)), "dashboards-custom", "vendored")

# The agentgateway board's guardrail row repeats the runtime board's "Guardrail checks"
# panel: dropped so each panel exists once.
GATEWAY_DUPLICATE_ROWS = ("Guardrails — what the secret / PII filters are catching",)


def _vendored(name):
    with open(os.path.join(VENDORED, name)) as f:
        return json.load(f)


def ai():
    d = Dash(UIDS["ai"], "AI / Gateway & Runtime",
             "The platform's AI plane end to end: agent runs (outcome, duration, steps), models (latency, errors, "
             "tokens, fallbacks), the seven MCP domain servers' tool calls, the agentgateway data plane (status, "
             "guardrails, shed load, xDS), circuit breakers, k8sgpt's explanations and the runtime's pods — then the "
             "gateway's own detail: LLM cost and time-to-first-token per model and MCP federation. Replaces 'Adhar "
             "AI' and 'Adhar AI Gateway (agentgateway)'.",
             tags("AI", "ai", "agentgateway"),
             [ds_var(), qvar("namespace", "Gateway namespace", "label_values(agentgateway_build_info, namespace)")],
             links=links("AI"), collapse={"At a glance"})
    d.embed(_vendored("adhar-ai.json"), var_map={"DS_PROMETHEUS": "datasource"})
    d.embed(_vendored("agentgateway.json"), row="Gateway detail", skip_rows=GATEWAY_DUPLICATE_ROWS)
    return d


DASHBOARDS = [
    ("ai", "AI", ai),
]
