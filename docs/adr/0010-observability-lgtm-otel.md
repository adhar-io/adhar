# ADR-0010: Observability — OpenTelemetry collection, Grafana LGTM storage, hub-and-spoke at scale

**Status**: Accepted (hub topology lands in Roadmap Phase 2) · **Date**: 2026-07

## Context

The platform promises "everything observable" (metrics, logs, traces, profiles, network flows) as a built-in output. The stack must be fully open-source, run scaled-down on a laptop, scale out to multi-cluster production, and avoid coupling application code to any vendor. A grab-bag of per-signal tools with separate UIs would fragment the operator experience.

## Decision

Standardize on **OpenTelemetry as the collection contract and the Grafana stack as storage/UX**:

- **Collection**: Grafana **Alloy** (OTel-native collector) is the single shipping agent for metrics, logs, and traces; **Beyla/Pixie** provide eBPF auto-instrumentation without code changes; **Hubble** covers network flows from the Cilium data path (ADR-0002)
- **Storage**: Prometheus for cluster-local metrics, **Mimir** for long-term/multi-cluster metrics, **Loki** for logs, **Tempo** for traces — all object-storage-backed in production so retention is policy, not disk size
- **UX**: Grafana is the single pane (dashboards, Explore, alerting); Alertmanager/Grafana OnCall route alerts; OpenCost attributes spend
- **Topology**: single-cluster (T1/T2) runs the full stack in-cluster; multi-cluster (T3) is **hub-and-spoke** — workload clusters run only collectors, the management cluster hosts storage and query
- All of it ships as ordinary packages (ADR-0004): swap or disable components per environment (e.g. `victoria-metrics` exists as an alternative package)

## Consequences

- ✅ Apps instrument once against OTel — no vendor coupling; eBPF options mean value before any instrumentation
- ✅ One UI and one query experience across signals and clusters; local and production observability are the same skills
- ⚠️ The full stack is heavy — the local curated core enables a subset (metrics-server, Hubble, Headlamp by default); enabling Mimir/Tempo locally needs resources
- ⚠️ Hub-and-spoke makes the management cluster's object storage the observability system of record — capacity planning and backup live there ([Production Guide](../PRODUCTION.md))
- ⚠️ Package-level pluggability means alternatives (e.g. VictoriaMetrics) are wired but not integration-tested to the same depth as the default path
