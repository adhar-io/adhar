#!/usr/bin/env python3
"""Builds the per-application Grafana dashboard that the Application composition
(platform/controlplane/configuration/compositions/apps/argocd-multienv.yaml)
composes for every custom application, and splices it into that composition
between the BEGIN/END GENERATED APP DASHBOARD markers.

    python3 hack/dashboards/build-app-dashboard.py

The dashboard is a Go-template RAW STRING (backticks) in the composition, so
Grafana's own `{{pod}}` legend syntax needs no escaping. The composition fills
in these placeholders per application:

    __APP__          application (XR) name — also the Rollout, the pods' prefix,
                     the Loki `app` label and the OTel service.name
    __POD_RE__       regex of the app's own pods: <app>-<pod-template-hash>-<suffix>
                     (a looser `<app>-.+-.+` also matched `<app>-db-1`, the
                     app's CNPG instance, and summed Postgres into the app's CPU)
    __ARGO_APP__     the Argo CD Application prefix `<project>-<app>-`
    __PROJECT__      Kargo project (= Argo CD AppProject)
    "__ENV_VAR__"    the `env` template variable object (project environments)
    "__LINKS__"      dashboard links (Argo CD / Kargo / Console / Gitea when the
                     public host is known, else only the Grafana ones)
    "__FRONTEND__"   the Frontend (browser) section: $appFrontendOpen (expanded,
                     for a browser app) or $appFrontendCollapsed (one closed row)

Every query was checked against live Spring/adhar-kit services
(transaction-service, gateway-service on DigitalOcean, 2026-10-23). Panels for a
capability an app does not have (no JVM, no Kafka, no traces yet) show their
`noValue` text explaining what feeds them, instead of an empty frame.
"""
import json
import os
import re
import sys

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
COMPOSITION = os.path.join(ROOT, "platform", "controlplane", "configuration", "compositions", "apps", "argocd-multienv.yaml")
BEGIN = "{{- /* BEGIN GENERATED APP DASHBOARD (hack/dashboards/build-app-dashboard.py) */ -}}"
END = "{{- /* END GENERATED APP DASHBOARD */ -}}"

PROM = {"type": "prometheus", "uid": "prometheus"}
MIMIR = {"type": "prometheus", "uid": "mimir"}
LOKI = {"type": "loki", "uid": "loki"}
TEMPO = {"type": "tempo", "uid": "tempo"}
GREEN, AMBER, RED, BLUE = "green", "#EAB839", "red", "#5794F2"

ENV = 'namespace="$env"'
PODS = 'pod=~"$pod"'
SEL = f'{ENV},{PODS}'
HTTP = f'{SEL},uri!~"/actuator.*"'
APP_CONTAINER = 'container!="",container!="POD"'


def steps(*pairs):
    return {"mode": "absolute", "steps": [{"color": c, "value": v} for v, c in pairs]}


class Board:
    def __init__(self):
        self.panels, self.row_panel = [], None
        self.y = self.x = self.row_h = 0
        self._id = 0

    def nid(self):
        self._id += 1
        return self._id

    def _newline(self):
        if self.x:
            self.y += self.row_h
            self.x = self.row_h = 0

    def _place(self, w, h):
        if self.x + w > 24:
            self._newline()
        gp = {"h": h, "w": w, "x": self.x, "y": self.y}
        self.x += w
        self.row_h = max(self.row_h, h)
        if self.x >= 24:
            self._newline()
        return gp

    def row(self, title, collapsed=False, repeat=None):
        self._newline()
        r = {"id": self.nid(), "type": "row", "title": title, "collapsed": collapsed,
             "gridPos": {"h": 1, "w": 24, "x": 0, "y": self.y}, "panels": []}
        if repeat:
            r["repeat"] = repeat
        self.panels.append(r)
        self.row_panel = r if collapsed else None
        self.y += 1

    def _add(self, p):
        (self.row_panel["panels"] if self.row_panel else self.panels).append(p)
        return p

    def _base(self, kind, title, desc, w, h, ds):
        p = {"id": self.nid(), "type": kind, "title": title, "description": desc, "gridPos": self._place(w, h)}
        if ds:
            p["datasource"] = ds
        return p

    @staticmethod
    def _t(ds, exprs, instant=False, fmt=None, exemplar=False):
        out = []
        for i, e in enumerate(exprs):
            expr, legend = e if isinstance(e, tuple) else (e, "")
            t = {"datasource": ds, "refId": chr(65 + i), "expr": expr, "legendFormat": legend or "__auto"}
            if instant:
                t.update({"instant": True, "range": False})
                if ds is LOKI:
                    t["queryType"] = "instant"
            if fmt:
                t["format"] = fmt
            if exemplar:
                t["exemplar"] = True
            out.append(t)
        return out

    def stat(self, title, expr, desc, unit="short", w=4, h=4, th=None, decimals=0, no_value="0",
             text_mode="value", color_mode="value", mappings=None, legend="", graph=False, ds=PROM, fields=""):
        p = self._base("stat", title, desc, w, h, ds)
        p["targets"] = self._t(ds, [(expr, legend)], instant=not graph, fmt="table" if fields else None)
        p["fieldConfig"] = {"defaults": {"unit": unit, "decimals": decimals, "noValue": no_value,
                                         "thresholds": th or steps((None, BLUE)), "color": {"mode": "thresholds"},
                                         "mappings": mappings or []}, "overrides": []}
        p["options"] = {"reduceOptions": {"calcs": ["lastNotNull"], "fields": fields, "values": False},
                        "colorMode": color_mode, "graphMode": "area" if graph else "none", "textMode": text_mode,
                        "justifyMode": "center", "orientation": "auto", "wideLayout": True, "showPercentChange": False}
        return self._add(p)

    def ts(self, title, exprs, desc, unit="short", w=12, h=8, ds=PROM, decimals=None, legend="list", stack=False,
           draw="line", no_value="No data", overrides=None, mx=None, exemplar=False, th=None):
        p = self._base("timeseries", title, desc, w, h, ds)
        p["targets"] = self._t(ds, exprs, exemplar=exemplar)
        custom = {"drawStyle": draw, "lineWidth": 1, "fillOpacity": 35 if stack else 12, "gradientMode": "opacity",
                  "showPoints": "never", "spanNulls": True, "axisSoftMin": 0,
                  "stacking": {"mode": "normal" if stack else "none", "group": "A"}}
        if th:
            custom["thresholdsStyle"] = {"mode": "dashed"}
        d = {"unit": unit, "custom": custom, "color": {"mode": "palette-classic"}, "noValue": no_value}
        if decimals is not None:
            d["decimals"] = decimals
        if mx is not None:
            d["max"] = mx
        if th:
            d["thresholds"] = th
        p["fieldConfig"] = {"defaults": d, "overrides": overrides or []}
        lg = {"showLegend": True, "displayMode": "list", "placement": "bottom", "calcs": []}
        if legend == "table":
            lg = {"showLegend": True, "displayMode": "table", "placement": "right", "calcs": ["lastNotNull", "max"],
                  "sortBy": "Max", "sortDesc": True}
        p["options"] = {"legend": lg, "tooltip": {"mode": "multi", "sort": "desc"}}
        return self._add(p)

    def table(self, title, queries, desc, w=24, h=8, rename=None, units=None, hide=None, sort=None, no_value="No data", ds=PROM):
        p = self._base("table", title, desc, w, h, ds)
        p["targets"] = [{"datasource": ds, "refId": chr(65 + i), "expr": e, "format": "table", "instant": True, "range": False}
                        for i, (e, _) in enumerate(queries)]
        if ds is LOKI:
            for t in p["targets"]:
                t["queryType"] = "instant"
        ren = dict(rename or {})
        if len(queries) == 1:
            ren["Value"] = queries[0][1]
        else:
            for i, (_, c) in enumerate(queries):
                ren[f"Value #{chr(65 + i)}"] = c
        excl = {"Time": True, **{k: True for k in (hide or [])}}
        p["transformations"] = ([{"id": "merge", "options": {}}] if len(queries) > 1 else []) + [
            {"id": "organize", "options": {"excludeByName": excl, "renameByName": ren}}]
        ov = []
        for col, (unit, th, mode) in (units or {}).items():
            props = [{"id": "unit", "value": unit}]
            if th:
                props += [{"id": "thresholds", "value": th}, {"id": "custom.cellOptions", "value": {"type": mode}}]
            ov.append({"matcher": {"id": "byName", "options": col}, "properties": props})
        p["fieldConfig"] = {"defaults": {"custom": {"align": "auto", "filterable": True, "cellOptions": {"type": "auto"}},
                                         "decimals": 2, "noValue": no_value}, "overrides": ov}
        p["options"] = {"showHeader": True, "cellHeight": "sm", "footer": {"show": False},
                        "sortBy": [{"displayName": sort[0], "desc": sort[1]}] if sort else []}
        return self._add(p)

    def state(self, title, expr, desc, w=12, h=6, legend="", mappings=None, th=None):
        p = self._base("state-timeline", title, desc, w, h, PROM)
        p["targets"] = self._t(PROM, [(expr, legend)])
        p["fieldConfig"] = {"defaults": {"color": {"mode": "thresholds"}, "thresholds": th or steps((None, RED), (1, GREEN)),
                                         "mappings": mappings or [], "noValue": "No pods",
                                         "custom": {"fillOpacity": 75, "lineWidth": 0}}, "overrides": []}
        p["options"] = {"showValue": "never", "mergeValues": True, "alignValue": "left", "rowHeight": 0.8,
                        "legend": {"showLegend": False, "displayMode": "list", "placement": "bottom"},
                        "tooltip": {"mode": "single", "sort": "none"}}
        return self._add(p)

    def logs(self, title, expr, desc, w=24, h=10):
        p = self._base("logs", title, desc, w, h, LOKI)
        p["targets"] = [{"datasource": LOKI, "refId": "A", "expr": expr, "queryType": "range"}]
        p["options"] = {"showTime": True, "wrapLogMessage": True, "prettifyLogMessage": False, "enableLogDetails": True,
                        "dedupStrategy": "none", "sortOrder": "Descending"}
        return self._add(p)

    def traces(self, title, traceql, desc, w=12, h=8, no_value="No traces yet: the app exports none to the platform collector"):
        p = self._base("table", title, desc, w, h, TEMPO)
        p["targets"] = [{"datasource": TEMPO, "refId": "A", "queryType": "traceql", "query": traceql, "limit": 20,
                         "tableType": "traces"}]
        p["fieldConfig"] = {"defaults": {"custom": {"align": "auto", "cellOptions": {"type": "auto"}},
                                         "noValue": no_value},
                            "overrides": []}
        p["options"] = {"showHeader": True, "cellHeight": "sm", "footer": {"show": False}}
        return self._add(p)

    def text(self, md, w=24, h=3):
        p = {"id": self.nid(), "type": "text", "title": "", "gridPos": self._place(w, h),
             "options": {"mode": "markdown", "content": md}}
        return self._add(p)


def co(name, color):
    return {"matcher": {"id": "byName", "options": name}, "properties": [{"id": "color", "value": {"mode": "fixed", "fixedColor": color}}]}


def rate(metric, sel=HTTP, by="", window="$__rate_interval"):
    b = f" by ({by})" if by else ""
    return f'sum{b} (rate({metric}{{{sel}}}[{window}]))'


def ratio(sel_extra, by="", window="$__rate_interval"):
    total = rate("http_server_requests_seconds_count", HTTP, by, window)
    part = rate("http_server_requests_seconds_count", f'{HTTP},{sel_extra}', by, window)
    return f"({part} or {total} * 0) / {total}"


# ----------------------------------------------------------------- frontend
# Browser telemetry from Grafana Faro (the golden-path React, Angular and TanStack
# templates load the Faro Web SDK). The platform's Faro receiver (an Alloy
# faro.receiver, package observability/faro) writes every browser signal to Loki
# as a logfmt line, labelled {source="faro", app_name, app_environment, kind}
# (its extra_log_labels), and browser spans to Tempo under service.name = the
# app. The kinds and the fields queried below are the receiver's own encoding
# (alloy internal/component/faro/receiver/internal/payload, v1.19):
#   kind="measurement" type="web-vitals"  lcp|inp|cls|fcp|ttfb=<value>
#   kind="event" event_name="faro.performance.navigation"   one per page load
#   kind="event" event_name="faro.performance.resource"     one per fetch/XHR:
#              event_data_initiatorType, event_data_duration (ms),
#              event_data_responseStatus, event_data_httpHost, event_data_name
#   kind="event" event_name="session_start"
#   kind="exception" type=<Error class> value=<message> stacktrace=...
# Every line also carries the page and session (page_url, session_id).
# Parse with a bare `| logfmt` or ONE named field: Loki 3.6 extracts only some of
# the fields of a multi-field `| logfmt a, b` (the second came back empty for
# both `type, value` and `event_name, event_data_responseStatus`), which left
# the error table without messages and the fetch-status panels empty.
FRONTEND_ROW = "Frontend (Faro: real users)"
FARO = 'source="faro",app_name="__APP__",app_environment="${env:text}"'
NO_FARO = "No browser telemetry: the page does not load the Faro Web SDK, or FARO_URL is unset (the golden-path frontends wire both)"
PAGE_LOADS = f'{{{FARO},kind="event"}} |= "faro.performance.navigation" | logfmt event_name | event_name="faro.performance.navigation"'
SESSIONS = f'{{{FARO},kind="event"}} |= "session_start" | logfmt event_name | event_name="session_start"'
ERRORS = f'{{{FARO},kind="exception"}}'
FETCH = (f'{{{FARO},kind="event"}} |= "faro.performance.resource" '
         '| logfmt '
         '| event_name="faro.performance.resource" | event_data_initiatorType=~"fetch|xmlhttprequest" '
         '| event_data_name!~".*/collect([?].*)?|.*/ingest/.*"')
FETCH_FAILED = f'{FETCH} | event_data_responseStatus=~"[45][0-9][0-9]"'
# web-vitals thresholds (good / poor), web.dev
VITALS = [("LCP", "lcp", "ms", 2500, 4000), ("INP", "inp", "ms", 200, 500), ("CLS", "cls", "none", 0.1, 0.25),
          ("FCP", "fcp", "ms", 1800, 3000), ("TTFB", "ttfb", "ms", 800, 1800)]


def vital(key, window, q=0.75):
    return (f'quantile_over_time({q}, {{{FARO},kind="measurement"}} |= "web-vitals" | logfmt '
            f'| type="web-vitals" | {key}!="" | unwrap {key} [{window}]) by (app_name)')


def count(sel, window, by=""):
    b = f" by ({by})" if by else ""
    return f"sum{b} (count_over_time({sel} [{window}]))"


def frontend(b):
    """The Frontend section: what real users experience in the browser."""
    b.row(FRONTEND_ROW)
    b.stat("Page Loads", count(PAGE_LOADS, "$__range"), "Full page loads in the selected range (Faro navigation timing).",
           ds=LOKI, no_value="—", w=4)
    b.stat("Sessions", count(SESSIONS, "$__range"), "Browser sessions started in the selected range.", ds=LOKI, no_value="—", w=4)
    b.stat("JS Errors", count(ERRORS, "$__range"), "Uncaught JavaScript errors and rejections reported by the browsers.",
           ds=LOKI, no_value="0", th=steps((None, GREEN), (1, AMBER), (25, RED)), w=4)
    b.stat("JS Errors / Page Load", f"({count(ERRORS, '$__range')} or vector(0)) / {count(PAGE_LOADS, '$__range')}",
           "JavaScript errors per page load. Above ~0.05 a noticeable share of visits break.", ds=LOKI, unit="none", decimals=3,
           no_value="—", th=steps((None, GREEN), (0.01, AMBER), (0.05, RED)), w=4)
    b.stat("Browser Fetch p95", f"quantile_over_time(0.95, {FETCH} | unwrap event_data_duration [$__range]) by (app_name)",
           "95th percentile of the page's own fetch/XHR calls as the browser measured them: network + backend + queueing.",
           ds=LOKI, unit="ms", decimals=0, no_value="—", th=steps((None, GREEN), (500, AMBER), (1500, RED)), w=4)
    b.stat("Browser Fetch Errors", f"({count(FETCH_FAILED, '$__range')} or vector(0)) / {count(FETCH, '$__range')}",
           "Share of the page's fetch/XHR calls answered 4xx/5xx (cross-origin calls without Timing-Allow-Origin report no status and count as success).",
           ds=LOKI, unit="percentunit", decimals=2, no_value="—", th=steps((None, GREEN), (0.01, AMBER), (0.05, RED)), w=4)
    for name, key, unit, good, poor in VITALS:
        b.stat(f"{name} p75", vital(key, "$__range"),
               f"75th percentile {name} over the selected range, as Core Web Vitals are judged: green ≤ {good}, red > {poor}"
               f"{' ms' if unit == 'ms' else ''}.", ds=LOKI, unit=unit, decimals=0 if unit == "ms" else 3, no_value="—",
               th=steps((None, GREEN), (good, AMBER), (poor, RED)), w=4)
    b.stat("Pages with Errors", f'count({count(ERRORS + " | logfmt page_url", "$__range", "page_url")})',
           "Distinct pages (URL) on which at least one JavaScript error was reported.", ds=LOKI, no_value="0",
           th=steps((None, GREEN), (1, AMBER)), w=4)
    b.ts("Page Loads & Sessions", [(count(PAGE_LOADS, "$__auto"), "page loads"), (count(SESSIONS, "$__auto"), "sessions")],
         "Page loads and new sessions per interval: real-user traffic as the browsers see it.", ds=LOKI, decimals=0, draw="bars",
         w=8, no_value=NO_FARO, overrides=[co("sessions", BLUE)])
    b.ts("Web Vitals p75", [(vital(key, "$__auto"), name) for name, key, unit, *_ in VITALS if unit == "ms"],
         "75th percentile LCP, INP, FCP and TTFB per interval, in milliseconds.", ds=LOKI, unit="ms", decimals=0, w=8,
         no_value=NO_FARO)
    b.ts("CLS p75", [(vital("cls", "$__auto"), "CLS")], "75th percentile Cumulative Layout Shift per interval (good ≤ 0.1).",
         ds=LOKI, decimals=3, w=8, no_value=NO_FARO, th=steps((None, GREEN), (0.1, AMBER), (0.25, RED)))
    b.ts("JS Errors by Type", [(count(ERRORS + " | logfmt type", "$__auto", "type"), "{{type}}")],
         "JavaScript errors per interval by error class.", ds=LOKI, decimals=0, draw="bars", stack=True, w=12,
         no_value="No JavaScript errors")
    b.table("Top JS Errors", [(f"topk(10, {count(ERRORS + ' | logfmt', '$__range', 'type, value')})", "Count")],
            "The most frequent error messages in the selected range. Open the Browser Errors logs below for stack traces.",
            w=12, ds=LOKI, rename={"type": "Type", "value": "Message"}, sort=("Count", True), no_value="No JavaScript errors")
    b.ts("Browser Fetch Latency", [
        (f"quantile_over_time({q}, {FETCH} | unwrap event_data_duration [$__auto]) by (app_name)", f"p{int(q * 100)}")
        for q in (0.5, 0.95)],
        "Duration of the page's fetch/XHR calls as measured in the browser (ms).", ds=LOKI, unit="ms", decimals=0, w=12,
        no_value=NO_FARO)
    b.ts("Browser Fetch by Host & Status", [(count(FETCH, "$__auto", "event_data_httpHost, event_data_responseStatus"),
                                            "{{event_data_httpHost}} {{event_data_responseStatus}}")],
         "Fetch/XHR calls per interval by the host called and the status it answered (0 = cross-origin, status hidden).",
         ds=LOKI, decimals=0, w=12, legend="table", no_value=NO_FARO)
    b.table("Top Pages", [(f"topk(15, {count(PAGE_LOADS + ' | logfmt page_url', '$__range', 'page_url')})", "Page Loads")],
            "The most loaded pages in the selected range.", w=12, ds=LOKI, rename={"page_url": "Page"},
            sort=("Page Loads", True), no_value=NO_FARO)
    b.traces("Browser Traces", '{resource.service.name="__APP__" && kind=client}',
             "Latest fetch/XHR spans from the browser. Their traceparent reached the backend, so each opens the full trace "
             "from the click through the services.", w=12,
             no_value="No browser traces yet: the page reports none (Faro's tracing instrumentation sends them)")
    b.logs("Browser Errors", f'{ERRORS} |~ "(?i)$search"',
           "JavaScript errors as the browsers reported them, with stack trace, page, browser and session. "
           "Filter with the 'Log search' box.", h=8)


def panels():
    b = Board()
    no_http = ("No HTTP metrics: the app serves none at /actuator/prometheus (adhar-kit / Micrometer apps do). "
               "A browser app's traffic is in the Frontend section")
    no_jvm = "Not a JVM app, or JVM metrics are not exported"

    # ------------------------------------------------------------- overview
    b.row("Overview")
    b.stat("Rollout", 'max by (phase) (rollout_info{namespace="$env",name="__APP__"})',
           "Argo Rollouts phase of the application in this environment (Healthy, Progressing, Paused for a canary step, Degraded).",
           text_mode="name", legend="{{phase}}", no_value="No Rollout",
           mappings=[{"type": "value", "options": {"1": {"color": BLUE}}}], color_mode="none")
    b.stat("Replicas", 'max(rollout_info_replicas_available{namespace="$env",name="__APP__"}) or max(kube_deployment_status_replicas_available{namespace="$env",deployment="__APP__"})',
           "Available replicas.", th=steps((None, RED), (1, GREEN)))
    b.stat("Argo CD", 'max by (health_status, sync_status) (argocd_app_info{name="__ARGO_APP__${env:text}"})',
           "Health and sync of the Argo CD Application __ARGO_APP__<env>.", text_mode="name", legend="{{health_status}} · {{sync_status}}",
           no_value="No Argo CD app", color_mode="none")
    b.stat("Requests / s", rate("http_server_requests_seconds_count", HTTP),
           "Request rate (excludes /actuator probes).", unit="reqps", decimals=2, no_value="—", graph=True)
    b.stat("5xx Ratio", ratio('status=~"5.."'), "Share of requests answered 5xx.", unit="percentunit", decimals=2, no_value="—",
           th=steps((None, GREEN), (0.001, AMBER), (0.01, RED)), graph=True)
    b.stat("p95 Latency", f'histogram_quantile(0.95, sum by (le) (rate(http_server_requests_seconds_bucket{{{HTTP}}}[$__rate_interval])))',
           "95th percentile server latency.", unit="s", decimals=3, no_value="—", th=steps((None, GREEN), (0.5, AMBER), (1, RED)), graph=True)
    b.stat("Error Budget Left", "1 - (" + ratio('status=~"5.."', window="$__range") + ") / (1 - $slo)",
           "Share of the error budget left over the selected range for the $slo availability objective (5xx counted as failures). Below 0 = spent.",
           unit="percentunit", decimals=1, no_value="—", th=steps((None, RED), (0.25, AMBER), (0.5, GREEN)))
    b.stat("Restarts", f'round(sum(increase(kube_pod_container_status_restarts_total{{{SEL},{APP_CONTAINER}}}[$__range]))) or vector(0)',
           "Container restarts in the selected range.", th=steps((None, GREEN), (1, AMBER), (5, RED)))
    b.stat("OOMKilled", f'count(kube_pod_container_status_last_terminated_reason{{{SEL},reason="OOMKilled"}} == 1) or vector(0)',
           "Containers whose last exit was an OOM kill.", th=steps((None, GREEN), (1, RED)))
    b.stat("Running Version", f'max by (version) (label_replace(kube_pod_container_info{{{SEL},container="app"}}, "version", "$1", "image_spec", ".*(?:@sha256:|:)([0-9a-f]{{12}}).*"))',
           "Image digest (first 12 hex) of the running app container; Kargo promotes by digest.", text_mode="name", legend="{{version}}",
           no_value="—", color_mode="none")
    b.stat("CPU / Request", f'sum(rate(container_cpu_usage_seconds_total{{{SEL},{APP_CONTAINER}}}[$__rate_interval])) / sum(kube_pod_container_resource_requests{{{SEL},{APP_CONTAINER},resource="cpu"}})',
           "CPU used over CPU requested.", unit="percentunit", decimals=0, th=steps((None, GREEN), (0.8, AMBER), (1.2, RED)), no_value="—")
    b.stat("Memory / Limit", f'sum(container_memory_working_set_bytes{{{SEL},{APP_CONTAINER}}}) / sum(kube_pod_container_resource_limits{{{SEL},{APP_CONTAINER},resource="memory"}})',
           "Working set over the memory limit. Near 100% = next OOM kill.", unit="percentunit", decimals=0,
           th=steps((None, GREEN), (0.75, AMBER), (0.9, RED)), no_value="No limit")

    # ------------------------------------------------------------- frontend
    frontend(b)

    # ------------------------------------------------------------- RED
    b.row("Golden Signals (RED)")
    b.ts("Requests / s by Status", [(rate("http_server_requests_seconds_count", HTTP, "status"), "{{status}}")],
         "Request rate by HTTP status (probes under /actuator excluded).", unit="reqps", decimals=2, no_value=no_http, w=8,
         overrides=[co("200", GREEN), co("500", RED), co("503", RED)])
    b.ts("Error Ratio (4xx / 5xx)", [(ratio('status=~"5.."'), "5xx"), (ratio('status=~"4.."'), "4xx")],
         "Share of requests answered 4xx (client) and 5xx (server).", unit="percentunit", decimals=2, no_value=no_http, w=8,
         overrides=[co("5xx", RED), co("4xx", AMBER)], th=steps((None, GREEN), (0.01, RED)))
    b.ts("Latency p50 / p95 / p99", [
        (f'histogram_quantile({q}, sum by (le) (rate(http_server_requests_seconds_bucket{{{HTTP}}}[$__rate_interval])))', f"p{int(q*100)}")
        for q in (0.5, 0.95, 0.99)],
        "Server latency percentiles from the Micrometer histogram. Diamonds are exemplars: click one to open that request's trace in Tempo.",
        unit="s", decimals=3, no_value=no_http, w=8, exemplar=True)
    b.table("Routes", [
        (rate("http_server_requests_seconds_count", HTTP, "method, uri"), "Req/s"),
        (ratio('status=~"5.."', "method, uri"), "5xx"),
        (ratio('status=~"4.."', "method, uri"), "4xx"),
        (f'histogram_quantile(0.95, sum by (method, uri, le) (rate(http_server_requests_seconds_bucket{{{HTTP}}}[$__rate_interval])))', "p95"),
        (f'histogram_quantile(0.99, sum by (method, uri, le) (rate(http_server_requests_seconds_bucket{{{HTTP}}}[$__rate_interval])))', "p99")],
        "Per route (method + URI template), current values.", h=8, rename={"method": "Method", "uri": "Route"},
        units={"Req/s": ("reqps", None, None),
               "5xx": ("percentunit", steps((None, GREEN), (0.001, AMBER), (0.01, RED)), "color-text"),
               "4xx": ("percentunit", steps((None, GREEN), (0.05, AMBER), (0.2, RED)), "color-text"),
               "p95": ("s", steps((None, GREEN), (0.5, AMBER), (1, RED)), "color-text"),
               "p99": ("s", steps((None, GREEN), (1, AMBER), (2, RED)), "color-text")},
        sort=("Req/s", True), no_value=no_http)
    b.ts("Latency Buckets (SLO)", [
        (f'sum(rate(http_server_requests_seconds_bucket{{{HTTP},le=~"0.1|0.25|0.5|1|1.0|2|2.0"}}[$__rate_interval])) by (le) / scalar({rate("http_server_requests_seconds_count", HTTP)})', "≤ {{le}}s")],
        "Share of requests faster than each SLO bucket (adhar-kit publishes 50ms…5s SLO buckets).", unit="percentunit", decimals=1,
        no_value=no_http, w=12, mx=1)
    b.ts("In-flight Requests", [(f'sum(http_server_requests_active_seconds_gcount{{{SEL}}})', "active")],
         "Requests being processed right now (Spring's active-request long task timer): saturation of the request path.",
         decimals=0, no_value=no_http, w=12)

    # ------------------------------------------------------------- resources
    b.row("Saturation & Resources")
    b.ts("CPU vs Request / Limit", [
        (f'sum by (pod) (rate(container_cpu_usage_seconds_total{{{SEL},{APP_CONTAINER}}}[$__rate_interval]))', "{{pod}}"),
        (f'max(kube_pod_container_resource_requests{{{SEL},{APP_CONTAINER},resource="cpu"}})', "request"),
        (f'max(kube_pod_container_resource_limits{{{SEL},{APP_CONTAINER},resource="cpu"}})', "limit")],
        "CPU cores per pod against the container's request and limit.", unit="none", decimals=2,
        overrides=[co("request", AMBER), co("limit", RED)])
    b.ts("Memory vs Request / Limit", [
        (f'sum by (pod) (container_memory_working_set_bytes{{{SEL},{APP_CONTAINER}}})', "{{pod}}"),
        (f'max(kube_pod_container_resource_requests{{{SEL},{APP_CONTAINER},resource="memory"}})', "request"),
        (f'max(kube_pod_container_resource_limits{{{SEL},{APP_CONTAINER},resource="memory"}})', "limit")],
        "Working set per pod against request and limit. Crossing the limit is an OOM kill.", unit="bytes", decimals=1,
        overrides=[co("request", AMBER), co("limit", RED)])
    b.ts("CPU Throttling", [(f'100 * sum by (pod) (rate(container_cpu_cfs_throttled_periods_total{{{SEL},{APP_CONTAINER}}}[$__rate_interval])) / clamp_min(sum by (pod) (rate(container_cpu_cfs_periods_total{{{SEL},{APP_CONTAINER}}}[$__rate_interval])), 1)', "{{pod}}")],
         "Share of CFS periods throttled. Sustained throttling inflates latency long before CPU looks full.", unit="percent", decimals=1, w=8)
    b.ts("Restarts", [(f'sum by (pod) (increase(kube_pod_container_status_restarts_total{{{SEL},{APP_CONTAINER}}}[$__rate_interval])) > 0', "{{pod}}")],
         "Container restarts per interval.", draw="bars", decimals=0, w=8, no_value="No restarts")
    b.ts("Replicas: Desired / Available / Updated", [
        ('max(rollout_info_replicas_desired{namespace="$env",name="__APP__"})', "desired"),
        ('max(rollout_info_replicas_available{namespace="$env",name="__APP__"})', "available"),
        ('max(rollout_info_replicas_updated{namespace="$env",name="__APP__"})', "updated (new version)"),
        ('max(kube_horizontalpodautoscaler_status_desired_replicas{namespace="$env",horizontalpodautoscaler="__APP__"})', "HPA desired")],
        "Rollout replicas. During a canary, 'updated' below 'desired' is the share of traffic on the new version; HPA desired is the autoscaler's target.",
        decimals=0, w=8, overrides=[co("available", GREEN), co("updated (new version)", BLUE), co("HPA desired", AMBER)])
    b.state("Pod Readiness", f'max by (pod) (kube_pod_status_ready{{{SEL},condition="true"}})',
            "Green = Ready. Red = running but failing its readiness probe (no traffic).", legend="{{pod}}",
            mappings=[{"type": "value", "options": {"0": {"text": "Not ready", "color": RED}, "1": {"text": "Ready", "color": GREEN}}}])
    b.ts("Network I/O", [
        (f'sum(rate(container_network_receive_bytes_total{{{SEL}}}[$__rate_interval]))', "receive"),
        (f'-sum(rate(container_network_transmit_bytes_total{{{SEL}}}[$__rate_interval]))', "transmit")],
        "Pod network throughput (transmit drawn below zero).", unit="Bps", decimals=1, h=6)

    # ------------------------------------------------------------- JVM
    b.row("JVM", collapsed=True)
    b.ts("Heap Used / Committed / Max", [
        (f'sum by (pod) (jvm_memory_used_bytes{{{SEL},area="heap"}})', "used {{pod}}"),
        (f'sum(jvm_memory_committed_bytes{{{SEL},area="heap"}})', "committed"),
        (f'sum(jvm_memory_max_bytes{{{SEL},area="heap"}} > 0)', "max")],
        "Java heap. Used touching max with frequent GC = undersized heap or a leak.", unit="bytes", decimals=1, no_value=no_jvm,
        overrides=[co("max", RED), co("committed", AMBER)])
    b.ts("Non-heap by Pool", [(f'sum by (id) (jvm_memory_used_bytes{{{SEL},area="nonheap"}})', "{{id}}")],
         "Metaspace, code cache and other non-heap pools.", unit="bytes", decimals=1, no_value=no_jvm, legend="table")
    b.ts("GC Pause Time", [
        (f'sum by (gc) (rate(jvm_gc_pause_seconds_sum{{{SEL}}}[$__rate_interval]))', "{{gc}} (s/s)"),
        (f'max by (gc) (jvm_gc_pause_seconds_max{{{SEL}}})', "{{gc}} max pause")],
        "Seconds spent paused in GC per second, and the longest recent pause.", unit="s", decimals=3, no_value=no_jvm, w=8)
    b.ts("Threads", [(f'sum by (state) (jvm_threads_states_threads{{{SEL}}})', "{{state}}")],
         "Live JVM threads by state. Growing BLOCKED/WAITING = contention or a stuck pool.", decimals=0, no_value=no_jvm, w=8, stack=True)
    b.ts("Classes Loaded & CPU", [
        (f'sum(jvm_classes_loaded_classes{{{SEL}}})', "classes loaded"),
        (f'100 * avg(process_cpu_usage{{{SEL}}})', "process CPU %")],
        "Loaded classes (a steady climb = classloader leak) and the JVM's own CPU view.", decimals=0, no_value=no_jvm, w=8)
    b.ts("DB Connection Pool (Hikari)", [
        (f'sum by (pool) (hikaricp_connections_active{{{SEL}}})', "{{pool}} active"),
        (f'sum by (pool) (hikaricp_connections_idle{{{SEL}}})', "{{pool}} idle"),
        (f'sum by (pool) (hikaricp_connections_pending{{{SEL}}})', "{{pool}} pending"),
        (f'max by (pool) (hikaricp_connections_max{{{SEL}}})', "{{pool}} max")],
        "JDBC pool usage. Pending > 0 means threads are waiting for a connection.", decimals=0,
        no_value="No JDBC pool in this app")
    b.ts("Connection Acquire Time", [
        (f'sum by (pool) (rate(hikaricp_connections_acquire_seconds_sum{{{SEL}}}[$__rate_interval])) / sum by (pool) (rate(hikaricp_connections_acquire_seconds_count{{{SEL}}}[$__rate_interval]))', "{{pool}} mean"),
        (f'sum by (pool) (increase(hikaricp_connections_timeout_total{{{SEL}}}[$__rate_interval]))', "{{pool}} timeouts")],
        "Mean time to borrow a connection, and acquisition timeouts.", unit="s", decimals=4, no_value="No JDBC pool in this app")
    b.ts("Kafka Consumer Lag", [(f'max by (topic) (kafka_consumer_fetch_manager_records_lag_max{{{SEL}}})', "{{topic}}")],
         "Max records behind per topic. Growing lag = consumers slower than producers.", decimals=0, w=8,
         no_value="No Kafka consumer in this app")
    b.ts("Kafka Listener", [
        (f'sum by (result) (rate(spring_kafka_listener_seconds_count{{{SEL}}}[$__rate_interval]))', "{{result}} /s"),
        (f'sum(rate(spring_kafka_listener_seconds_sum{{{SEL}}}[$__rate_interval])) / sum(rate(spring_kafka_listener_seconds_count{{{SEL}}}[$__rate_interval]))', "mean time (s)")],
        "@KafkaListener invocations per second by result, and mean processing time.", decimals=3, w=8,
        no_value="No @KafkaListener in this app", overrides=[co("failure /s", RED)])
    b.ts("Cache / Valkey Commands", [
        (f'sum by (command) (rate(lettuce_command_completion_seconds_count{{{SEL}}}[$__rate_interval]))', "{{command}} /s")],
        "Valkey (Redis protocol) commands per second through Lettuce (Spring Data Redis).", decimals=1, w=8, legend="table",
        no_value="No Valkey/Redis client metrics in this app")
    b.ts("Log Events by Level (Logback)", [(f'sum by (level) (rate(logback_events_total{{{SEL}}}[$__rate_interval]))', "{{level}}")],
         "Log events emitted per second by level, counted inside the JVM.", unit="ops", decimals=2, no_value=no_jvm,
         overrides=[co("error", RED), co("warn", AMBER)])
    b.ts("Executor Pools", [
        (f'sum by (name) (executor_active_threads{{{SEL}}})', "{{name}} active"),
        (f'sum by (name) (executor_queued_tasks{{{SEL}}})', "{{name}} queued")],
        "Spring task executors: active threads and queued tasks.", decimals=0, no_value="No executor metrics")

    # ------------------------------------------------------------- business
    b.row("Business Metrics (adhar-kit)", collapsed=True)
    b.text("Application-specific metrics appear here **automatically**: anything the app publishes under the "
           "**adhar.business.*** name (Prometheus **adhar_business_***) — via **@BusinessMetric**, or "
           "**AdharMetrics** counters, timers and gauges from adhar-kit — gets a panel per metric below, "
           "split by its **category** and **outcome** tags. Nothing to configure on the dashboard.", h=3)
    rep = b.ts("$business_metric", [(f'sum by (category, outcome) (rate({{__name__="$business_metric",{SEL}}}[$__rate_interval]))', "{{category}} {{outcome}}")],
               "Per-second rate of this business counter (or timer count), by category and outcome.", unit="ops", decimals=2, w=8,
               no_value="No adhar_business_* metrics from this app yet")
    rep["repeat"] = "business_metric"
    rep["repeatDirection"] = "h"
    rep["maxPerRow"] = 3
    b.table("All Business Series (current)", [(f'{{__name__=~"adhar_business_.+",__name__!~".+_bucket",{SEL}}}', "Value")],
            "Every adhar_business_* series and its current value: counters' totals, gauges, summaries.", h=8,
            hide=["container", "endpoint", "instance", "job", "namespace", "service", "environment"],
            rename={"__name__": "Metric"}, no_value="No adhar_business_* metrics from this app yet")

    # ------------------------------------------------------------- logs
    b.row("Logs (Loki)", collapsed=True)
    b.ts("Log Lines by Level", [('sum by (detected_level) (count_over_time({namespace="$env", app="__APP__", pod=~"$pod"} [$__auto]))', "{{detected_level}}")],
         "Lines per interval by the level Loki detected in the JSON/logfmt 'level' field.", ds=LOKI, decimals=0, draw="bars", stack=True,
         overrides=[co("error", RED), co("fatal", RED), co("critical", RED), co("warn", AMBER), co("info", GREEN)], w=12, no_value="No logs")
    b.ts("Error Lines", [('sum(count_over_time({namespace="$env", app="__APP__", pod=~"$pod"} | detected_level=~"error|fatal|critical" [$__auto]))', "errors")],
         "Error-level log lines per interval.", ds=LOKI, decimals=0, draw="bars", w=12, overrides=[co("errors", RED)], no_value="No error lines")
    b.logs("Logs", '{namespace="$env", app="__APP__", pod=~"$pod"} |~ "(?i)$search"',
           "The app's own log lines (search with the 'Log search' box, a case-insensitive regex). A traceId field links to the trace in Tempo.")

    # ------------------------------------------------------------- traces
    b.row("Traces (Tempo)", collapsed=True)
    span = 'service="__APP__",span_kind=~"SPAN_KIND_SERVER|SPAN_KIND_CONSUMER"'
    no_traces = "No traces yet: the app exports none to the platform collector (adhar-kit sends OTLP to Alloy)"
    b.ts("Traced Requests / s", [(f'sum by (status_code) (rate(traces_spanmetrics_calls_total{{{span}}}[$__rate_interval]))', "{{status_code}}")],
         "Server/consumer spans per second from Tempo's span metrics (in Mimir), by span status.", ds=MIMIR, unit="reqps", decimals=2, w=8,
         no_value=no_traces, overrides=[co("STATUS_CODE_ERROR", RED), co("STATUS_CODE_OK", GREEN)])
    b.ts("Traced Error Ratio", [(f'sum(rate(traces_spanmetrics_calls_total{{{span},status_code="STATUS_CODE_ERROR"}}[$__rate_interval])) / sum(rate(traces_spanmetrics_calls_total{{{span}}}[$__rate_interval]))', "error ratio")],
         "Share of server spans with error status.", ds=MIMIR, unit="percentunit", decimals=2, w=8, no_value=no_traces, overrides=[co("error ratio", RED)])
    b.ts("Traced Latency p95 by Operation", [(f'histogram_quantile(0.95, sum by (span_name, le) (rate(traces_spanmetrics_latency_bucket{{{span}}}[$__rate_interval])))', "{{span_name}}")],
         "p95 span duration per operation.", ds=MIMIR, unit="s", decimals=3, w=8, legend="table", no_value=no_traces)
    b.traces("Recent Traces", '{resource.service.name="__APP__"}', "Latest traces of this service. Click a trace ID to open it.")
    b.traces("Error Traces", '{resource.service.name="__APP__" && status=error}', "Latest traces containing an error span.")
    b.traces("Slow Traces (> 1s)", '{resource.service.name="__APP__" && duration > 1s}', "Latest traces slower than one second.", w=24)
    return b.panels


def dashboard():
    return {
        "uid": "app-__APP__",
        "title": "__APP__ · application",
        "description": "Service dashboard of the custom application __APP__, composed by the Application composition "
                       "(golden signals, resources, JVM, business metrics, logs, traces, delivery). Generated by "
                       "hack/dashboards/build-app-dashboard.py.",
        "tags": ["adhar", "application", "__APP__"],
        "editable": False,
        "graphTooltip": 1,
        "schemaVersion": 39,
        "time": {"from": "now-3h", "to": "now"},
        "refresh": "30s",
        "links": "__LINKS__",
        "annotations": {"list": [
            {"builtIn": 1, "datasource": {"type": "grafana", "uid": "-- Grafana --"}, "enable": True, "hide": True,
             "iconColor": "rgba(0, 211, 255, 1)", "name": "Annotations & Alerts", "type": "dashboard"},
            {"datasource": PROM, "enable": True, "iconColor": "blue", "name": "Deploys",
             "expr": '(time() - max(kube_replicaset_created{namespace="$env",replicaset=~"__APP__-[a-z0-9]{6,10}"})) < 60',
             "step": "30s", "titleFormat": "New version rolled out", "textFormat": "A new ReplicaSet of __APP__ (Kargo promotion or rollout)"},
            {"datasource": PROM, "enable": True, "iconColor": "purple", "name": "Argo CD syncs",
             "expr": 'sum by (phase) (increase(argocd_app_sync_total{name="__ARGO_APP__${env:text}"}[2m])) > 0',
             "step": "1m", "titleFormat": "Argo CD sync", "textFormat": "{{phase}}"},
            {"datasource": PROM, "enable": True, "iconColor": "red", "name": "Restarts",
             "expr": f'sum(increase(kube_pod_container_status_restarts_total{{{ENV},pod=~"__POD_RE__",container!=""}}[2m])) > 0',
             "step": "1m", "titleFormat": "Container restarted", "textFormat": ""},
        ]},
        "templating": {"list": [
            "__ENV_VAR__",
            {"name": "pod", "label": "Pod", "type": "query", "datasource": PROM,
             "definition": 'label_values(kube_pod_info{namespace="$env",pod=~"__POD_RE__"}, pod)',
             "query": {"qryType": 1, "query": 'label_values(kube_pod_info{namespace="$env",pod=~"__POD_RE__"}, pod)', "refId": "var-pod"},
             "includeAll": True, "multi": True, "allValue": "__POD_RE__", "refresh": 2, "sort": 1,
             "current": {"text": ["All"], "value": ["$__all"]}, "options": [], "hide": 0},
            {"name": "business_metric", "label": "Business metric", "type": "query", "datasource": PROM,
             "definition": 'label_values({__name__=~"adhar_business_.+_(total|count)",namespace="$env",pod=~"$pod"}, __name__)',
             "query": {"qryType": 1, "query": 'label_values({__name__=~"adhar_business_.+_(total|count)",namespace="$env",pod=~"$pod"}, __name__)', "refId": "var-business_metric"},
             "includeAll": True, "multi": True, "refresh": 2, "sort": 1, "hide": 2,
             "current": {"text": ["All"], "value": ["$__all"]}, "options": []},
            {"name": "slo", "label": "Availability SLO", "type": "custom", "query": "0.99,0.995,0.999",
             "current": {"text": "0.995", "value": "0.995"}, "includeAll": False, "multi": False, "hide": 0,
             "options": [{"text": v, "value": v, "selected": v == "0.995"} for v in ("0.99", "0.995", "0.999")]},
            {"name": "search", "label": "Log search", "type": "textbox", "query": "", "current": {"text": "", "value": ""}, "hide": 0},
        ]},
        "panels": panels(),
    }


LINKS_KNOWN_HOST = [
    {"title": "Argo CD", "type": "link", "icon": "external link", "targetBlank": True, "includeVars": False, "keepTime": False,
     "url": "https://argocd.__PUBLIC_HOST__/applications/adhar-system/__ARGO_APP__${env:text}", "tooltip": "This environment's Argo CD Application"},
    {"title": "Kargo", "type": "link", "icon": "external link", "targetBlank": True, "includeVars": False, "keepTime": False,
     "url": "https://kargo.__PUBLIC_HOST__/project/__PROJECT__", "tooltip": "Promotion pipeline (dev → test → prod)"},
    {"title": "Repository", "type": "link", "icon": "external link", "targetBlank": True, "includeVars": False, "keepTime": False,
     "url": "__REPO_WEB__", "tooltip": "Source repository in Gitea"},
    {"title": "Console", "type": "link", "icon": "external link", "targetBlank": True, "includeVars": False, "keepTime": False,
     "url": "https://console.__PUBLIC_HOST__/catalog", "tooltip": "Adhar Console catalog"},
]
LINKS_ALWAYS = [
    {"title": "Adhar Platform", "type": "link", "icon": "dashboard", "targetBlank": False, "includeVars": False, "keepTime": True,
     "url": "/d/adhar-platform-overview/adhar-platform", "tooltip": "The platform front page"},
    {"title": "Applications", "type": "dashboards", "asDropdown": True, "icon": "external link", "tags": ["application"],
     "targetBlank": False, "includeVars": False, "keepTime": True, "url": "", "tooltip": "Other application dashboards"},
]


def compact(v, depth=0):
    """JSON with one panel / variable / annotation / link per line: reviewable in a
    diff without tripling the composition's size the way indented JSON does."""
    one = lambda x: json.dumps(x, ensure_ascii=False, separators=(",", ":"))
    pad = " " * depth
    if isinstance(v, list) and v and all(isinstance(x, (dict, str)) for x in v):
        items = []
        for x in v:
            if isinstance(x, dict) and x.get("type") == "row" and x.get("panels"):
                row = dict(x)
                kids = row.pop("panels")
                items.append(pad + " " + one(row)[:-1] + ',"panels":' + compact(kids, depth + 1) + "}")
            else:
                items.append(pad + " " + one(x))
        return "[\n" + ",\n".join(items) + "\n" + pad + "]"
    if isinstance(v, dict):
        parts = []
        for k, x in v.items():
            if k in ("panels", "list", "templating", "annotations", "links") and isinstance(x, (list, dict)):
                parts.append(f'{pad} {one(k)}:' + compact(x, depth + 1))
            else:
                parts.append(f"{pad} {one(k)}:{one(x)}")
        return "{\n" + ",\n".join(parts) + "\n" + pad + "}"
    return one(v)


def split_frontend(dash):
    """Takes the Frontend section out of the panel list, leaving the "__FRONTEND__"
    placeholder, and returns it twice: expanded (the row followed by its panels,
    for a browser app) and collapsed (one closed row holding them). The layout is
    computed with the section expanded; when it is collapsed Grafana moves the
    panels below it up into the gap."""
    ps = dash["panels"]
    i = next(k for k, p in enumerate(ps) if p.get("type") == "row" and p["title"] == FRONTEND_ROW)
    j = next((k for k in range(i + 1, len(ps)) if ps[k].get("type") == "row"), len(ps))
    row, kids = ps[i], ps[i + 1:j]
    dash["panels"] = ps[:i] + ["__FRONTEND__"] + ps[j:]
    one = lambda x: json.dumps(x, ensure_ascii=False, separators=(",", ":"))
    pad = "  "
    opened = (",\n" + pad).join(one(x) for x in [row] + kids)
    closed = one(dict(row, collapsed=True))[:-1] + ',"panels":[\n' + pad + " " + (",\n" + pad + " ").join(one(x) for x in kids) + "]}"
    return opened, closed


def snippet():
    """The Go-template block placed in the composition between the markers."""
    board = dashboard()
    front_open, front_closed = split_frontend(board)
    dash = compact(board)
    links_host = compact(LINKS_KNOWN_HOST + LINKS_ALWAYS)
    links_always = compact(LINKS_ALWAYS)
    for s in (dash, links_host, links_always, front_open, front_closed):
        if "`" in s:
            sys.exit("a backtick in the dashboard JSON would end the Go raw string")
    ind = " " * 12

    def raw(var, s):
        lines = s.splitlines()
        return f"{ind}{{{{- {var} := `" + lines[0] + "\n" + "\n".join(ind + l for l in lines[1:]) + "` -}}\n"

    return (f"{ind}{BEGIN}\n"
            + raw("$appDash", dash)
            + raw("$appLinksHost", links_host)
            + raw("$appLinks", links_always)
            + raw("$appFrontendOpen", front_open)
            + raw("$appFrontendCollapsed", front_closed)
            + f"{ind}{END}\n")


def main():
    body = open(COMPOSITION).read()
    start = body.index(" " * 12 + BEGIN)
    end = body.index(END, start) + len(END) + 1
    new = body[:start] + snippet() + body[end:]
    if "--check" in sys.argv:
        if new != body:
            sys.exit("argocd-multienv.yaml is stale: run hack/dashboards/build-app-dashboard.py")
        return
    open(COMPOSITION, "w").write(new)
    print(f"updated {COMPOSITION}")


if __name__ == "__main__":
    main()
