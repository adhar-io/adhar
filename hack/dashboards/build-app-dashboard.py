#!/usr/bin/env python3
"""Builds the per-application Grafana dashboards that the Application composition
(platform/controlplane/configuration/compositions/apps/argocd-multienv.yaml)
composes for every custom application, and splices them into that composition
between the BEGIN/END GENERATED APP DASHBOARD markers.

    python3 hack/dashboards/build-app-dashboard.py           # regenerate
    python3 hack/dashboards/build-app-dashboard.py --check   # fail if stale
    python3 hack/dashboards/build-app-dashboard.py --dump DIR  # write each type's JSON

ONE TEMPLATE PER KIND OF APPLICATION. A backend has no browser, a single-page
app has no JVM: one dashboard for every app made each of them scroll past
sections that could never have data. The four types, and what is specific to
each (everything else is shared):

    backend-jvm      Spring Boot / adhar-kit, Quarkus: Micrometer RED, JVM
                     (heap, GC, threads, pools), JDBC/Hikari, Kafka, Valkey,
                     adhar-kit business metrics.
    backend-generic  Go, Python, Node.js or anything else: RED from whichever
                     convention the app emits (Micrometer-named Prometheus
                     metrics, or OTLP metrics in Mimir under the OpenTelemetry
                     HTTP semantic conventions, old and new), runtime panels for
                     Node.js / Go / Python / process metrics, container pressure.
    frontend         SPA / static site: Faro real-user monitoring (Core Web
                     Vitals, JS errors by page/browser/release, sessions, browser
                     fetches by host) and what the pod's nginx served.
    fullstack        TanStack Start: the frontend sections plus the Node.js
                     server's RED (prom-client, Micrometer names) and runtime.

Shared by all: a colour-coded health row on top, Saturation & Resources, Logs,
Traces and dependencies (Tempo span metrics + service graph), deploy / sync /
restart annotations, links, and the env / pod variables.

WHICH TEMPLATE AN APP GETS is decided in the composition (see the selection block
after the markers): spec.parameters.dashboard.type, else the XR label
adhar.io/dashboard-type, else a browser app (type `web` or adhar.io/frontend:
"true") is `frontend`, else `backend-generic`.

Every template is a Go-template RAW STRING (backticks) in the composition, so
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

Queries were checked against the live cluster (DigitalOcean, 2026-10-23):
transaction-service (Spring/adhar-kit) for the JVM, scraped-RED and dependency
panels, reactapp (React SPA behind nginx) for the served-traffic panels; every
query was also run for syntax against its datasource. Panels for a capability
an app does not have show their `noValue` text explaining what feeds them,
instead of an empty frame.
"""
import json
import os
import sys

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
COMPOSITION = os.path.join(ROOT, "platform", "controlplane", "configuration", "compositions", "apps", "argocd-multienv.yaml")
BEGIN = "{{- /* BEGIN GENERATED APP DASHBOARD (hack/dashboards/build-app-dashboard.py) */ -}}"
END = "{{- /* END GENERATED APP DASHBOARD */ -}}"

TYPES = ("backend-jvm", "backend-generic", "frontend", "fullstack")
# Go-template variable each type's JSON is bound to in the composition.
TEMPLATE_VAR = {"backend-jvm": "$appDashBackendJvm", "backend-generic": "$appDashBackendGeneric",
                "frontend": "$appDashFrontend", "fullstack": "$appDashFullstack"}

PROM = {"type": "prometheus", "uid": "prometheus"}
MIMIR = {"type": "prometheus", "uid": "mimir"}
LOKI = {"type": "loki", "uid": "loki"}
TEMPO = {"type": "tempo", "uid": "tempo"}
MIXED = {"type": "datasource", "uid": "-- Mixed --"}
GREEN, AMBER, ORANGE, RED, BLUE, PURPLE = "green", "#EAB839", "#FF9830", "red", "#5794F2", "#B877D9"

ENV = 'namespace="$env"'
PODS = 'pod=~"$pod"'
SEL = f'{ENV},{PODS}'
# Probe and scrape endpoints are not traffic: Spring's /actuator, Quarkus' /q,
# the conventional /health*, /livez|/readyz and the metrics endpoint itself.
PROBES = "/actuator.*|/q/(health|metrics).*|/health.*|/livez|/readyz|/metrics"
HTTP = f'{SEL},uri!~"{PROBES}"'
APP_CONTAINER = 'container!="",container!="POD"'
W = "$__rate_interval"

# OTLP metrics reach Mimir through Alloy's otelcol.exporter.prometheus with
# resource_to_telemetry_conversion, so every resource attribute is a label:
# service.name → service_name, k8s.namespace.name → k8s_namespace_name. The
# golden-path templates put the pod's namespace and name into
# OTEL_RESOURCE_ATTRIBUTES; an app that does not has no namespace label, and the
# `|` in the regex keeps its series (all environments together) rather than
# showing nothing.
OSEL = 'service_name="__APP__",k8s_namespace_name=~"$env|",k8s_pod_name=~"$pod|"'
# The HTTP server metric conventions an OTLP-instrumented app may emit:
# (metric, divisor to seconds, status label, route labels, method label)
OTLP_HTTP = [
    ("http_server_request_duration_seconds", 1, "http_response_status_code", ["http_route"], "http_request_method"),  # semconv ≥ 1.23 (Go otelhttp, opt-in elsewhere)
    ("http_server_duration_milliseconds", 1000, "http_status_code", ["http_target", "http_route"], "http_method"),  # old semconv (Python/Node auto-instrumentation)
    ("http_server_requests_milliseconds", 1000, "status", ["uri"], "method"),  # Micrometer OTLP registry (Quarkus), base unit ms
    ("http_server_requests_seconds", 1, "status", ["uri"], "method"),  # Micrometer OTLP in seconds
]


def steps(*pairs):
    return {"mode": "absolute", "steps": [{"color": c, "value": v} for v, c in pairs]}


GOOD_HIGH = steps((None, RED), (0.9, AMBER), (0.99, GREEN))


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

    def row(self, title, collapsed=True):
        self._newline()
        r = {"id": self.nid(), "type": "row", "title": title, "collapsed": collapsed,
             "gridPos": {"h": 1, "w": 24, "x": 0, "y": self.y}, "panels": []}
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
        """Targets. An entry is an expression, (expr, legend) or (expr, legend, datasource);
        a target with its own datasource makes the panel a Mixed one."""
        out = []
        for i, e in enumerate(exprs):
            if not isinstance(e, tuple):
                e = (e, "")
            expr, legend, tds = (e + (None,))[:3]
            tds = tds or ds
            t = {"datasource": tds, "refId": chr(65 + i), "expr": expr, "legendFormat": legend or "__auto"}
            if instant:
                t.update({"instant": True, "range": False})
                if tds is LOKI:
                    t["queryType"] = "instant"
            elif tds is LOKI:
                t["queryType"] = "range"
            if fmt:
                t["format"] = fmt
            if exemplar and tds is not LOKI:
                t["exemplar"] = True
            out.append(t)
        return out

    @staticmethod
    def _ds_of(ds, exprs):
        own = {id(e[2]) for e in exprs if isinstance(e, tuple) and len(e) > 2 and e[2] is not None}
        return MIXED if own and own != {id(ds)} else ds

    def stat(self, title, expr, desc, unit="short", w=3, h=4, th=None, decimals=0, no_value="0",
             text_mode="value", color_mode="background", mappings=None, legend="", graph=False, ds=PROM):
        exprs = expr if isinstance(expr, list) else [(expr, legend)]
        pds = self._ds_of(ds, exprs)
        p = self._base("stat", title, desc, w, h, pds)
        p["targets"] = self._t(ds, exprs, instant=not graph)
        p["fieldConfig"] = {"defaults": {"unit": unit, "decimals": decimals, "noValue": no_value,
                                         "thresholds": th or steps((None, BLUE)), "color": {"mode": "thresholds"},
                                         "mappings": mappings or []}, "overrides": []}
        p["options"] = {"reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
                        "colorMode": color_mode, "graphMode": "area" if graph else "none", "textMode": text_mode,
                        "justifyMode": "center", "orientation": "auto", "wideLayout": True, "showPercentChange": False}
        return self._add(p)

    def ts(self, title, exprs, desc, unit="short", w=12, h=8, ds=PROM, decimals=None, legend="list", stack=False,
           draw="line", no_value="No data", overrides=None, mx=None, exemplar=False, th=None):
        pds = self._ds_of(ds, exprs)
        p = self._base("timeseries", title, desc, w, h, pds)
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
        """queries: [(expr, column)] or [(expr, column, datasource)]."""
        pds = self._ds_of(ds, queries)
        p = self._base("table", title, desc, w, h, pds)
        p["targets"] = []
        for i, q in enumerate(queries):
            tds = q[2] if len(q) > 2 and q[2] is not None else ds
            t = {"datasource": tds, "refId": chr(65 + i), "expr": q[0], "format": "table", "instant": True, "range": False}
            if tds is LOKI:
                t["queryType"] = "instant"
            p["targets"].append(t)
        ren = dict(rename or {})
        if len(queries) == 1:
            ren["Value"] = queries[0][1]
        else:
            for i, q in enumerate(queries):
                ren[f"Value #{chr(65 + i)}"] = q[1]
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

    def state(self, title, expr, desc, w=12, h=6, legend="", mappings=None, th=None, unit=None, ds=PROM,
              no_value="No pods", show_value="never"):
        p = self._base("state-timeline", title, desc, w, h, ds)
        p["targets"] = self._t(ds, [(expr, legend)])
        d = {"color": {"mode": "thresholds"}, "thresholds": th or steps((None, RED), (1, GREEN)),
             "mappings": mappings or [], "noValue": no_value, "custom": {"fillOpacity": 75, "lineWidth": 0}}
        if unit:
            d["unit"] = unit
        p["fieldConfig"] = {"defaults": d, "overrides": []}
        p["options"] = {"showValue": show_value, "mergeValues": True, "alignValue": "left", "rowHeight": 0.8,
                        "legend": {"showLegend": False, "displayMode": "list", "placement": "bottom"},
                        "tooltip": {"mode": "single", "sort": "none"}}
        return self._add(p)

    def heatmap(self, title, expr, desc, w=12, h=8, ds=PROM, unit="s", no_value="No data"):
        p = self._base("heatmap", title, desc, w, h, ds)
        p["targets"] = [{"datasource": ds, "refId": "A", "expr": expr, "format": "heatmap", "legendFormat": "{{le}}",
                         "exemplar": True}]
        p["fieldConfig"] = {"defaults": {"noValue": no_value, "custom": {"scaleDistribution": {"type": "linear"},
                                                                         "hideFrom": {"legend": False, "tooltip": False, "viz": False}}},
                            "overrides": []}
        p["options"] = {"calculate": False, "cellGap": 1, "showValue": "never",
                        "color": {"mode": "scheme", "scheme": "Spectral", "reverse": True, "steps": 64, "exponent": 0.5,
                                  "fill": ORANGE, "scale": "exponential"},
                        "yAxis": {"axisPlacement": "left", "unit": unit, "reverse": False},
                        "rowsFrame": {"layout": "auto"}, "filterValues": {"le": 1e-09},
                        "tooltip": {"mode": "single", "yHistogram": True, "showColorScale": True},
                        "legend": {"show": True}, "exemplars": {"color": "rgba(255,0,255,0.7)"}}
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


def rate(metric, sel=HTTP, by="", window=W):
    b = f" by ({by})" if by else ""
    return f'sum{b} (rate({metric}{{{sel}}}[{window}]))'


def ratio(sel_extra, by="", window=W):
    total = rate("http_server_requests_seconds_count", HTTP, by, window)
    part = rate("http_server_requests_seconds_count", f'{HTTP},{sel_extra}', by, window)
    return f"({part} or {total} * 0) / {total}"


def quantile(q, by="", window=W):
    byl = ", ".join(([by] if by else []) + ["le"])
    return f'histogram_quantile({q}, sum by ({byl}) (rate(http_server_requests_seconds_bucket{{{HTTP}}}[{window}])))'


# ---------------------------------------------------------------- OTLP (Mimir)
def _osel(conv, extra=""):
    _, _, _, routes, _ = conv
    s = OSEL + "".join(f',{r}!~"{PROBES}"' for r in routes)
    return s + (f",{extra}" if extra else "")


def _norm(expr, conv):
    """Renames a convention's labels to status / route / method."""
    _, _, st, routes, meth = conv
    e = expr
    if st != "status":
        e = f'label_replace({e}, "status", "$1", "{st}", "(.+)")'
    for r in routes:
        e = f'label_replace({e}, "route", "$1", "{r}", "(.+)")'
    if meth != "method":
        e = f'label_replace({e}, "method", "$1", "{meth}", "(.+)")'
    return e


def otlp_rate(by="", status_re=None, window=W):
    b = f" by ({by})" if by else ""
    parts = []
    for conv in OTLP_HTTP:
        extra = f'{conv[2]}=~"{status_re}"' if status_re else ""
        parts.append(f"sum{b} ({_norm(f'rate({conv[0]}_count{{{_osel(conv, extra)}}}[{window}])', conv)})")
    return "(" + " or ".join(parts) + ")"


def otlp_ratio(status_re, by="", window=W):
    total = otlp_rate(by, window=window)
    return f"({otlp_rate(by, status_re, window)} or {total} * 0) / {total}"


def otlp_quantile(q, by="", window=W):
    byl = ", ".join(([by] if by else []) + ["le"])
    parts = []
    for conv in OTLP_HTTP:
        e = f"histogram_quantile({q}, sum by ({byl}) ({_norm(f'rate({conv[0]}_bucket{{{_osel(conv)}}}[{window}])', conv)}))"
        parts.append(f"({e} / {conv[1]})" if conv[1] != 1 else e)
    return "(" + " or ".join(parts) + ")"


# ---------------------------------------------------------------- frontend (Faro)
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
# Every line also carries the meta: page_url, session_id, app_version,
# browser_name, browser_os, browser_mobile.
# Parse with a bare `| logfmt` or ONE named field per stage: Loki 3.6 extracts
# only some of the fields of a multi-field `| logfmt a, b` (the second came back
# empty for both `type, value` and `event_name, event_data_responseStatus`).
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
VITAL_LINES = f'{{{FARO},kind="measurement"}} |= "web-vitals" | logfmt | type="web-vitals"'
# web-vitals thresholds (good / poor), web.dev
VITALS = [("LCP", "lcp", "ms", 2500, 4000), ("INP", "inp", "ms", 200, 500), ("CLS", "cls", "none", 0.1, 0.25),
          ("FCP", "fcp", "ms", 1800, 3000), ("TTFB", "ttfb", "ms", 800, 1800)]

# ---------------------------------------------------------------- app logs (Loki)
# Alloy labels every stream of a WORKLOAD namespace with the application, its
# component (app | db | cache | events: the app's own pods, its CNPG cluster,
# Valkey, Kafka) and the environment NAME (the namespace's adhar.io/environment,
# i.e. ${env:text}, not the namespace). Lines ingested before those labels
# existed carry only namespace/app/pod: LEGACY selects exactly those
# (adhar_application="" = the label is absent), so a range spanning the rollout
# neither loses nor double-counts lines. The pod filter applies to the app's own
# pods only; $pod never matches a db/cache/events pod.
APP_LOGS = ('{adhar_application="__APP__", adhar_environment="${env:text}", adhar_component=~"$component"}'
            ' | pod=~"$pod" or adhar_component!="app"')
APP_LOGS_LEGACY = '{namespace="$env", app="__APP__", pod=~"$pod", adhar_application=""}'
APP_ONLY = '{adhar_application="__APP__", adhar_environment="${env:text}", adhar_component="app"} | pod=~"$pod"'


def logq(fn, pipeline="", window="$__auto", legacy=True, sel=APP_LOGS):
    """fn(range_expr) for the labelled streams, OR'd with the same over the
    legacy streams: their label sets differ, so `or` is a union, and the
    aggregation around it sums both."""
    new = fn(f"{sel}{pipeline} [{window}]")
    if not legacy:
        return new
    return f"({new} or {fn(f'{APP_LOGS_LEGACY}{pipeline} [{window}]')})"


# The pod's own nginx access log (combined format): the app component only.
# (a double-quoted pattern: a backtick would end the composition's Go raw string)
NGINX_PIPE = (' != "kube-probe" '
              r'| pattern "<_> - <_> [<_>] \"<method> <path> <_>\" <status> <bytes> \"<_>\" \"<agent>\"" | status=~"[0-9]+"')


def nginx(fn, extra="", window="$__auto"):
    return logq(fn, NGINX_PIPE + extra, window, sel=APP_ONLY)
NO_NGINX = "No access-log lines: the pod's server does not log in nginx's combined format (the golden-path SPAs do)"


def vital(key, window, q=0.75, by="app_name"):
    return (f'quantile_over_time({q}, {VITAL_LINES} | {key}!="" | unwrap {key} [{window}]) by ({by})')


def vital_good(key, good, window="$__range"):
    total = f'sum(count_over_time({VITAL_LINES} | {key}!="" [{window}]))'
    return f'(sum(count_over_time({VITAL_LINES} | {key}!="" | {key} <= {good} [{window}])) or {total} * 0) / {total}'


def count(sel, window, by=""):
    b = f" by ({by})" if by else ""
    return f"sum{b} (count_over_time({sel} [{window}]))"


VITAL_MAP = {name: (key, unit, good, poor) for name, key, unit, good, poor in VITALS}


def vital_stat(b, name, w=3):
    key, unit, good, poor = VITAL_MAP[name]
    b.stat(f"{name} p75", vital(key, "$__range"),
           f"75th percentile {name} over the selected range, as Core Web Vitals are judged: green ≤ {good}, red > {poor}"
           f"{' ms' if unit == 'ms' else ''}.", ds=LOKI, unit=unit, decimals=0 if unit == "ms" else 3, no_value="—",
           th=steps((None, GREEN), (good, AMBER), (poor, RED)), w=w)


def frontend(b, server_side=False):
    """What real users experience in the browser: Faro RUM, then errors, network,
    pages and releases (each a collapsed detail row)."""
    b.row(FRONTEND_ROW, collapsed=False)
    b.stat("Page Loads", count(PAGE_LOADS, "$__range"), "Full page loads in the selected range (Faro navigation timing).",
           ds=LOKI, no_value="—", color_mode="value", w=4)
    b.stat("Sessions", count(SESSIONS, "$__range"), "Browser sessions started in the selected range.", ds=LOKI,
           no_value="—", color_mode="value", w=4)
    b.stat("Pages / Session", f"{count(PAGE_LOADS, '$__range')} / {count(SESSIONS, '$__range')}",
           "Page loads per session: how deep visitors go. A single-page app counts only full loads, not client-side route changes.",
           ds=LOKI, unit="none", decimals=1, no_value="—", color_mode="value", w=4)
    b.stat("Sessions with an Error", f'count({count(ERRORS + " | logfmt session_id", "$__range", "session_id")}) / {count(SESSIONS, "$__range")}',
           "Share of sessions in which at least one JavaScript error was reported.", ds=LOKI, unit="percentunit",
           decimals=1, no_value="—", th=steps((None, GREEN), (0.01, AMBER), (0.05, RED)), w=4)
    b.stat("Browser Fetch p95", f"quantile_over_time(0.95, {FETCH} | unwrap event_data_duration [$__range]) by (app_name)",
           "95th percentile of the page's own fetch/XHR calls as the browser measured them: network + backend + queueing.",
           ds=LOKI, unit="ms", decimals=0, no_value="—", th=steps((None, GREEN), (500, AMBER), (1500, RED)), w=4)
    b.stat("Pages with Errors", f'count({count(ERRORS + " | logfmt page_url", "$__range", "page_url")})',
           "Distinct pages (URL) on which at least one JavaScript error was reported.", ds=LOKI, no_value="0",
           th=steps((None, GREEN), (1, AMBER)), w=4)
    for name, key, unit, good, poor in VITALS:
        b.stat(f"{name} good", vital_good(key, good),
               f"Share of {name} measurements in the 'good' band (≤ {good}{' ms' if unit == 'ms' else ''}). "
               "Core Web Vitals pass when at least 75% of visits are good.",
               ds=LOKI, unit="percentunit", decimals=0, no_value="—", th=steps((None, RED), (0.5, AMBER), (0.75, GREEN)),
               w=4 if name == "TTFB" else 5)
    b.ts("Page Loads & Sessions", [(count(PAGE_LOADS, "$__auto"), "page loads"), (count(SESSIONS, "$__auto"), "sessions")],
         "Page loads and new sessions per interval: real-user traffic as the browsers see it.", ds=LOKI, decimals=0, draw="bars",
         w=8, no_value=NO_FARO, overrides=[co("sessions", BLUE)])
    b.ts("Web Vitals p75", [(vital(key, "$__auto"), name) for name, key, unit, *_ in VITALS if unit == "ms"],
         "75th percentile LCP, INP, FCP and TTFB per interval, in milliseconds. Dashed lines: the 'good' thresholds of LCP (2.5 s) and INP (200 ms).",
         ds=LOKI, unit="ms", decimals=0, w=8, no_value=NO_FARO, th=steps((None, GREEN), (200, AMBER), (2500, RED)))
    b.ts("CLS p75", [(vital("cls", "$__auto"), "CLS")], "75th percentile Cumulative Layout Shift per interval (good ≤ 0.1, poor > 0.25).",
         ds=LOKI, decimals=3, w=8, no_value=NO_FARO, th=steps((None, GREEN), (0.1, AMBER), (0.25, RED)))

    # ---------------------------------------------------------- errors
    b.row("Browser Errors")
    b.ts("JS Errors by Type", [(count(ERRORS + " | logfmt type", "$__auto", "type"), "{{type}}")],
         "JavaScript errors per interval by error class.", ds=LOKI, decimals=0, draw="bars", stack=True, w=12,
         no_value="No JavaScript errors")
    b.ts("JS Errors by Browser", [(count(ERRORS + " | logfmt browser_name", "$__auto", "browser_name"), "{{browser_name}}")],
         "JavaScript errors per interval by browser: an error confined to one browser is a compatibility bug.", ds=LOKI,
         decimals=0, draw="bars", stack=True, w=12, no_value="No JavaScript errors")
    b.table("Top JS Errors", [(f"topk(10, {count(ERRORS + ' | logfmt', '$__range', 'type, value')})", "Count")],
            "The most frequent error messages in the selected range. Open the Browser Errors logs below for stack traces.",
            w=12, ds=LOKI, rename={"type": "Type", "value": "Message"}, sort=("Count", True), no_value="No JavaScript errors")
    b.table("Error Rate by Page", [
        (count(ERRORS + " | logfmt page_url", "$__range", "page_url"), "JS Errors", LOKI),
        (count(PAGE_LOADS + " | logfmt page_url", "$__range", "page_url"), "Page Loads", LOKI),
        (f'{count(ERRORS + " | logfmt page_url", "$__range", "page_url")} / {count(PAGE_LOADS + " | logfmt page_url", "$__range", "page_url")}',
         "Errors / Load", LOKI)],
        "Per page: errors, loads and errors per load. The page with the highest ratio is where users are breaking.",
        w=12, ds=LOKI, rename={"page_url": "Page"}, sort=("JS Errors", True), no_value="No JavaScript errors",
        units={"Errors / Load": ("none", steps((None, GREEN), (0.01, AMBER), (0.05, RED)), "color-background")})
    b.logs("Browser Errors", f'{ERRORS} |~ "(?i)$search"',
           "JavaScript errors as the browsers reported them, with stack trace, page, browser and session. "
           "Filter with the 'Log search' box.", h=8)

    # ---------------------------------------------------------- network
    b.row("Browser Network (fetch / XHR)")
    b.ts("Browser Fetch Latency", [
        (f"quantile_over_time({q}, {FETCH} | unwrap event_data_duration [$__auto]) by (app_name)", f"p{int(q * 100)}")
        for q in (0.5, 0.95)],
        "Duration of the page's fetch/XHR calls as measured in the browser (ms).", ds=LOKI, unit="ms", decimals=0, w=12,
        no_value=NO_FARO)
    b.ts("Browser Fetch by Host & Status", [(count(FETCH, "$__auto", "event_data_httpHost, event_data_responseStatus"),
                                            "{{event_data_httpHost}} {{event_data_responseStatus}}")],
         "Fetch/XHR calls per interval by the host called and the status it answered (0 = cross-origin, status hidden).",
         ds=LOKI, decimals=0, w=12, legend="table", no_value=NO_FARO)
    b.table("Fetch by Host", [
        (count(FETCH, "$__range", "event_data_httpHost"), "Calls", LOKI),
        (f"({count(FETCH_FAILED, '$__range', 'event_data_httpHost')} or {count(FETCH, '$__range', 'event_data_httpHost')} * 0)"
         f" / {count(FETCH, '$__range', 'event_data_httpHost')}", "Failed", LOKI),
        (f"quantile_over_time(0.5, {FETCH} | unwrap event_data_duration [$__range]) by (event_data_httpHost)", "p50", LOKI),
        (f"quantile_over_time(0.95, {FETCH} | unwrap event_data_duration [$__range]) by (event_data_httpHost)", "p95", LOKI)],
        "Every host the page calls, with volume, failure share and latency as the browser saw it: the slow or failing "
        "dependency of a frontend is usually one of these.", w=12, ds=LOKI, rename={"event_data_httpHost": "Host"},
        sort=("Calls", True), no_value=NO_FARO,
        units={"Failed": ("percentunit", steps((None, GREEN), (0.01, AMBER), (0.05, RED)), "color-text"),
               "p50": ("ms", None, None),
               "p95": ("ms", steps((None, GREEN), (500, AMBER), (1500, RED)), "color-text")})
    b.table("Slowest Calls", [(f"topk(15, quantile_over_time(0.95, {FETCH} | unwrap event_data_duration [$__range]) by (event_data_name))", "p95")],
            "The fetch/XHR URLs with the highest p95 in the selected range.", w=12, ds=LOKI, rename={"event_data_name": "URL"},
            sort=("p95", True), no_value=NO_FARO, units={"p95": ("ms", steps((None, GREEN), (500, AMBER), (1500, RED)), "color-text")})
    b.traces("Browser Traces", '{resource.service.name="__APP__" && kind=client}',
             "Latest fetch/XHR spans from the browser. Their traceparent reached the backend, so each opens the full trace "
             "from the click through the services.", w=12,
             no_value="No browser traces yet: the page reports none (Faro's tracing instrumentation sends them)")
    b.traces("Slow Browser Requests (> 1s)", '{resource.service.name="__APP__" && kind=client && duration > 1s}',
             "Browser requests slower than one second, with their full trace.", w=12,
             no_value="No browser request slower than one second")

    # ---------------------------------------------------------- pages & releases
    b.row("Pages, Sessions & Releases")
    b.table("Top Pages", [
        (f"topk(15, {count(PAGE_LOADS + ' | logfmt page_url', '$__range', 'page_url')})", "Page Loads", LOKI),
        (vital("lcp", "$__range", by="page_url"), "LCP p75", LOKI),
        (vital("ttfb", "$__range", by="page_url"), "TTFB p75", LOKI)],
        "The most loaded pages with their LCP and TTFB at p75.", w=12, ds=LOKI, rename={"page_url": "Page"},
        sort=("Page Loads", True), no_value=NO_FARO,
        units={"LCP p75": ("ms", steps((None, GREEN), (2500, AMBER), (4000, RED)), "color-text"),
               "TTFB p75": ("ms", steps((None, GREEN), (800, AMBER), (1800, RED)), "color-text")})
    b.table("Release Comparison (app version)", [
        (count(PAGE_LOADS + " | logfmt app_version", "$__range", "app_version"), "Page Loads", LOKI),
        (count(ERRORS + " | logfmt app_version", "$__range", "app_version"), "JS Errors", LOKI),
        (vital("lcp", "$__range", by="app_version"), "LCP p75", LOKI),
        (vital("inp", "$__range", by="app_version"), "INP p75", LOKI),
        (vital("cls", "$__range", by="app_version"), "CLS p75", LOKI)],
        "The same signals per version of the app (Faro's app.version, the package.json version the build injects): "
        "did the last release make things better or worse?", w=12, ds=LOKI, rename={"app_version": "Version"},
        sort=("Version", True), no_value=NO_FARO,
        units={"LCP p75": ("ms", steps((None, GREEN), (2500, AMBER), (4000, RED)), "color-text"),
               "INP p75": ("ms", steps((None, GREEN), (200, AMBER), (500, RED)), "color-text"),
               "CLS p75": ("none", steps((None, GREEN), (0.1, AMBER), (0.25, RED)), "color-text")})
    b.ts("Sessions by Browser", [(count(SESSIONS + " | logfmt browser_name", "$__auto", "browser_name"), "{{browser_name}}")],
         "New sessions per interval by browser.", ds=LOKI, decimals=0, draw="bars", stack=True, w=8, no_value=NO_FARO)
    b.ts("Sessions by OS & Device", [(count(SESSIONS + " | logfmt browser_os", "$__auto", "browser_os"), "{{browser_os}}")],
         "New sessions per interval by operating system.", ds=LOKI, decimals=0, draw="bars", stack=True, w=8, no_value=NO_FARO)
    b.ts("LCP p75 by Version", [(vital("lcp", "$__auto", by="app_version"), "{{app_version}}")],
         "LCP at p75 per app version over time: a regression shows as a new version's line above the old one.",
         ds=LOKI, unit="ms", decimals=0, w=8, no_value=NO_FARO, th=steps((None, GREEN), (2500, AMBER), (4000, RED)))

    if not server_side:
        served_by_nginx(b)


def served_by_nginx(b):
    b.row("Served by the Pod (nginx)")
    cnt = lambda r: f"count_over_time({r})"
    rate_ = lambda r: f"rate({r})"
    total = f"sum({nginx(rate_)})"
    b.ts("Requests / s by Status", [(f'sum by (status) {nginx(rate_)}', "{{status}}")],
         "What the pod's nginx served per second, by status, from its access log (kubelet probes excluded). This is the "
         "static bundle and /runtime-config.json; API calls are in Browser Network.", ds=LOKI, unit="reqps", decimals=2, w=8,
         no_value=NO_NGINX, overrides=[co("200", GREEN), co("304", BLUE), co("404", AMBER), co("500", RED), co("502", RED)])
    b.ts("4xx / 5xx Share", [
        (f'(sum({nginx(rate_, " | status=~\"5..\"")}) or {total} * 0) / {total}', "5xx"),
        (f'(sum({nginx(rate_, " | status=~\"4..\"")}) or {total} * 0) / {total}', "4xx")],
        "Share of served requests answered 4xx (a missing asset or a stale bundle hash) and 5xx.", ds=LOKI,
        unit="percentunit", decimals=2, w=8, no_value=NO_NGINX, overrides=[co("5xx", RED), co("4xx", AMBER)])
    b.ts("Bytes Served", [(f'sum({nginx(lambda r: f"sum_over_time({r})", " | unwrap bytes")})', "bytes")],
         "Response bytes per interval: a jump after a release is a bundle that grew.", ds=LOKI, unit="decbytes", decimals=1,
         w=8, draw="bars", no_value=NO_NGINX)
    b.table("Not Found (404) Paths", [(f'topk(15, sum by (path) {nginx(cnt, " | status=\"404\"", "$__range")})', "Count")],
            "Paths answered 404: broken links, or browsers asking for assets of a bundle that is no longer deployed.",
            w=12, ds=LOKI, rename={"path": "Path"}, sort=("Count", True), no_value="No 404s")
    b.table("Top Paths", [(f'topk(15, sum by (path) {nginx(cnt, "", "$__range")})', "Requests")],
            "The most requested paths.", w=12, ds=LOKI, rename={"path": "Path"}, sort=("Requests", True), no_value=NO_NGINX)


# ---------------------------------------------------------------- overview
def rollout_code():
    sel = 'namespace="$env",name="__APP__"'
    return (f'(max(rollout_info{{{sel},phase=~"Healthy|Completed"}}) * 1) or (max(rollout_info{{{sel},phase="Progressing"}}) * 2)'
            f' or (max(rollout_info{{{sel},phase="Paused"}}) * 3) or (max(rollout_info{{{sel},phase="Degraded"}}) * 4)'
            ' or ((max(kube_deployment_status_replicas_available{namespace="$env",deployment="__APP__"}) >= 0) * 0 + 5)')


ROLLOUT_MAP = [{"type": "value", "options": {
    "1": {"text": "Healthy", "color": GREEN, "index": 0}, "2": {"text": "Progressing", "color": BLUE, "index": 1},
    "3": {"text": "Paused (canary step)", "color": AMBER, "index": 2}, "4": {"text": "Degraded", "color": RED, "index": 3},
    "5": {"text": "Deployment", "color": BLUE, "index": 4}}}]


def argocd_code():
    sel = 'name="__ARGO_APP__${env:text}"'
    return (f'((max(argocd_app_info{{{sel},health_status="Healthy"}}) * 1) or (max(argocd_app_info{{{sel},health_status="Progressing"}}) * 2)'
            f' or (max(argocd_app_info{{{sel},health_status=~"Suspended|Missing|Unknown"}}) * 3) or (max(argocd_app_info{{{sel},health_status="Degraded"}}) * 4))'
            f' + ((max(argocd_app_info{{{sel},sync_status="OutOfSync"}}) * 10) or vector(0))')


ARGO_MAP = [{"type": "value", "options": {
    "1": {"text": "Healthy · Synced", "color": GREEN, "index": 0}, "11": {"text": "Healthy · OutOfSync", "color": AMBER, "index": 1},
    "2": {"text": "Progressing", "color": BLUE, "index": 2}, "12": {"text": "Progressing · OutOfSync", "color": BLUE, "index": 3},
    "3": {"text": "Missing / Unknown", "color": AMBER, "index": 4}, "13": {"text": "Missing · OutOfSync", "color": AMBER, "index": 5},
    "4": {"text": "Degraded", "color": RED, "index": 6}, "14": {"text": "Degraded · OutOfSync", "color": RED, "index": 7}}}]


def overview_delivery(b, w=3):
    b.stat("Rollout", rollout_code(),
           "Argo Rollouts phase of the application in this environment: Healthy, Progressing (a new version rolling out), "
           "Paused (a canary step waiting) or Degraded.", mappings=ROLLOUT_MAP, no_value="No Rollout", w=w,
           th=steps((None, BLUE)))
    b.stat("Argo CD", argocd_code(), "Health and sync of the Argo CD Application __ARGO_APP__<env>.", mappings=ARGO_MAP,
           no_value="No Argo CD app", w=w)
    b.stat("Pods Ready", f'sum(kube_pod_status_ready{{{SEL},condition="true"}}) / clamp_min(max(rollout_info_replicas_desired{{namespace="$env",name="__APP__"}}) or max(kube_deployment_spec_replicas{{namespace="$env",deployment="__APP__"}}), 1)',
           "Ready pods over desired replicas. Below 100% = pods failing readiness or not scheduled.", unit="percentunit",
           th=steps((None, RED), (0.5, AMBER), (1, GREEN)), no_value="No pods", w=w)


def overview_runtime(b, w=3, only=("Restarts", "OOMKilled", "CPU / Request", "Memory / Limit", "CPU Throttled")):
    """The pod-health stats, in the order given (each type lays its top rows out differently)."""
    stats = {
        "Restarts": lambda: b.stat(
            "Restarts", f'round(sum(increase(kube_pod_container_status_restarts_total{{{SEL},{APP_CONTAINER}}}[$__range]))) or vector(0)',
            "Container restarts in the selected range.", th=steps((None, GREEN), (1, AMBER), (5, RED)), w=w),
        "OOMKilled": lambda: b.stat(
            "OOMKilled", f'count(kube_pod_container_status_last_terminated_reason{{{SEL},reason="OOMKilled"}} == 1) or vector(0)',
            "Containers whose last exit was an OOM kill.", th=steps((None, GREEN), (1, RED)), w=w),
        "CPU / Request": lambda: b.stat(
            "CPU / Request", f'sum(rate(container_cpu_usage_seconds_total{{{SEL},{APP_CONTAINER}}}[{W}])) / sum(kube_pod_container_resource_requests{{{SEL},{APP_CONTAINER},resource="cpu"}})',
            "CPU used over CPU requested.", unit="percentunit", decimals=0, th=steps((None, GREEN), (0.8, AMBER), (1.2, RED)),
            no_value="—", w=w),
        "Memory / Limit": lambda: b.stat(
            "Memory / Limit", f'sum(container_memory_working_set_bytes{{{SEL},{APP_CONTAINER}}}) / sum(kube_pod_container_resource_limits{{{SEL},{APP_CONTAINER},resource="memory"}})',
            "Working set over the memory limit. Near 100% = next OOM kill.", unit="percentunit", decimals=0,
            th=steps((None, GREEN), (0.75, AMBER), (0.9, RED)), no_value="No limit", w=w),
        "CPU Throttled": lambda: b.stat(
            "CPU Throttled", f'sum(rate(container_cpu_cfs_throttled_periods_total{{{SEL},{APP_CONTAINER}}}[{W}])) / clamp_min(sum(rate(container_cpu_cfs_periods_total{{{SEL},{APP_CONTAINER}}}[{W}])), 1)',
            "Share of CFS periods in which the app was throttled by its CPU limit. Sustained throttling inflates latency long before CPU looks full.",
            unit="percentunit", decimals=0, th=steps((None, GREEN), (0.1, AMBER), (0.25, RED)), no_value="—", w=w),
    }
    for name in only:
        stats[name]()


def overview_version(b, w=6):
    b.stat("Running Version", f'max by (version) (label_replace(kube_pod_container_info{{{SEL},container="app"}}, "version", "$1", "image_spec", ".*(?:@sha256:|:)([0-9a-f]{{12}}).*"))',
           "Image digest (first 12 hex) of the running app container; Kargo promotes by digest.", text_mode="name",
           legend="{{version}}", no_value="—", w=w, th=steps((None, PURPLE)))


def overview_server_red(b, generic, w=3, prefix=""):
    """Requests/s, 5xx, p95, error budget, burn: the backend's health at a glance."""
    if generic:
        rps = [(rate("http_server_requests_seconds_count", HTTP), "", PROM), (otlp_rate(), "", MIMIR)]
        err = [(ratio('status=~"5.."'), "", PROM), (otlp_ratio("5.."), "", MIMIR)]
        p95 = [(quantile(0.95), "", PROM), (otlp_quantile(0.95), "", MIMIR)]
        left = [("1 - (" + ratio('status=~"5.."', window="$__range") + ") / (1 - $slo)", "", PROM),
                ("1 - (" + otlp_ratio("5..", window="$__range") + ") / (1 - $slo)", "", MIMIR)]
        burn = [(f"({ratio('status=~\"5..\"', window='1h')}) / (1 - $slo)", "", PROM),
                (f"({otlp_ratio('5..', window='1h')}) / (1 - $slo)", "", MIMIR)]
    else:
        rps = [(rate("http_server_requests_seconds_count", HTTP), "")]
        err = [(ratio('status=~"5.."'), "")]
        p95 = [(quantile(0.95), "")]
        left = [("1 - (" + ratio('status=~"5.."', window="$__range") + ") / (1 - $slo)", "")]
        burn = [(f"({ratio('status=~\"5..\"', window='1h')}) / (1 - $slo)", "")]
    b.stat(f"{prefix}Requests / s", rps, "Request rate (probe and metrics endpoints excluded).", unit="reqps", decimals=2,
           no_value="—", graph=True, color_mode="value", w=w)
    b.stat(f"{prefix}5xx Ratio", err, "Share of requests answered 5xx.", unit="percentunit", decimals=2, no_value="—",
           th=steps((None, GREEN), (0.001, AMBER), (0.01, RED)), w=w)
    b.stat(f"{prefix}p95 Latency", p95, "95th percentile server latency.", unit="s", decimals=3, no_value="—",
           th=steps((None, GREEN), (0.5, AMBER), (1, RED)), w=w)
    b.stat("Error Budget Left", left,
           "Share of the error budget left over the selected range for the $slo availability objective (5xx counted as failures). Below 0 = spent.",
           unit="percentunit", decimals=1, no_value="—", th=steps((None, RED), (0.25, AMBER), (0.5, GREEN)), w=w)
    b.stat("Burn Rate (1h)", burn,
           "How fast the last hour spends the error budget: 1 = exactly on budget, ≥ 14.4 pages (2% of a 30-day budget in an hour), ≥ 6 is a ticket.",
           unit="none", decimals=1, no_value="—", th=steps((None, GREEN), (1, AMBER), (6, ORANGE), (14.4, RED)), w=w)


def overview(b, kind):
    b.row("Overview", collapsed=False)
    if kind in ("backend-jvm", "backend-generic"):
        overview_delivery(b)                                    # 3 × 3
        overview_server_red(b, generic=kind == "backend-generic")  # 5 × 3  → 24
        overview_runtime(b)                                     # 5 × 3
        overview_version(b, w=9)                                # → 24
    elif kind == "frontend":
        overview_delivery(b)
        b.stat("JS Errors / Page Load", f"({count(ERRORS, '$__range')} or vector(0)) / {count(PAGE_LOADS, '$__range')}",
               "JavaScript errors per page load. Above ~0.05 a noticeable share of visits break.", ds=LOKI, unit="none",
               decimals=3, no_value="—", th=steps((None, GREEN), (0.01, AMBER), (0.05, RED)))
        b.stat("Browser Fetch Errors", f"({count(FETCH_FAILED, '$__range')} or vector(0)) / {count(FETCH, '$__range')}",
               "Share of the page's fetch/XHR calls answered 4xx/5xx (cross-origin calls without Timing-Allow-Origin report no status and count as success).",
               ds=LOKI, unit="percentunit", decimals=2, no_value="—", th=steps((None, GREEN), (0.01, AMBER), (0.05, RED)))
        served = lambda extra: f'sum({nginx(lambda r: f"count_over_time({r})", extra, "$__range")})'
        b.stat("Served 5xx", f'({served(" | status=~\"5..\"")} or vector(0)) / {served("")}',
               "Share of requests the pod's nginx answered 5xx (access log, probes excluded).", ds=LOKI, unit="percentunit",
               decimals=2, no_value="—", th=steps((None, GREEN), (0.001, AMBER), (0.01, RED)))
        b.stat("Served 4xx", f'({served(" | status=~\"4..\"")} or vector(0)) / {served("")}',
               "Share of requests the pod's nginx answered 4xx: missing assets, often a stale bundle after a release.",
               ds=LOKI, unit="percentunit", decimals=2, no_value="—", th=steps((None, GREEN), (0.02, AMBER), (0.1, RED)))
        for name in ("LCP", "INP", "CLS", "FCP", "TTFB"):     # 21 + 3 → 24, then 12
            vital_stat(b, name)
        overview_runtime(b, only=("Restarts", "OOMKilled", "CPU / Request", "Memory / Limit"))  # → 24
        overview_runtime(b, only=("CPU Throttled",))
        overview_version(b, w=21)                               # → 24
    else:  # fullstack
        overview_delivery(b)                                    # 9
        overview_server_red(b, generic=False, prefix="Server ")  # 15 → 24
        b.stat("JS Errors / Page Load", f"({count(ERRORS, '$__range')} or vector(0)) / {count(PAGE_LOADS, '$__range')}",
               "JavaScript errors per page load. Above ~0.05 a noticeable share of visits break.", ds=LOKI, unit="none",
               decimals=3, no_value="—", th=steps((None, GREEN), (0.01, AMBER), (0.05, RED)))
        for name in ("LCP", "INP", "CLS"):                      # 3 + 9 → 12
            vital_stat(b, name)
        b.stat("Browser Fetch Errors", f"({count(FETCH_FAILED, '$__range')} or vector(0)) / {count(FETCH, '$__range')}",
               "Share of the page's fetch/XHR calls answered 4xx/5xx (cross-origin calls without Timing-Allow-Origin report no status and count as success).",
               ds=LOKI, unit="percentunit", decimals=2, no_value="—", th=steps((None, GREEN), (0.01, AMBER), (0.05, RED)))
        overview_runtime(b, only=("Restarts", "OOMKilled", "Memory / Limit"))  # 15 + 9 → 24
        overview_runtime(b, only=("CPU / Request", "CPU Throttled"))
        overview_version(b, w=18)                               # → 24


# ---------------------------------------------------------------- golden signals
def golden_signals_scraped(b, title="Golden Signals (RED)", collapsed=False, no_http=None):
    """Micrometer-named RED from Prometheus: Spring/adhar-kit, and any app that
    publishes http_server_requests_seconds (the TanStack server does, by design)."""
    no_http = no_http or ("No HTTP metrics: the app publishes no http_server_requests_seconds "
                          "(adhar-kit / Micrometer apps and the TanStack server do)")
    b.row(title, collapsed=collapsed)
    b.ts("Requests / s by Status", [(rate("http_server_requests_seconds_count", HTTP, "status"), "{{status}}")],
         "Request rate by HTTP status (probe and metrics endpoints excluded).", unit="reqps", decimals=2, no_value=no_http, w=8,
         overrides=[co("200", GREEN), co("500", RED), co("503", RED)])
    b.ts("Error Ratio (4xx / 5xx)", [(ratio('status=~"5.."'), "5xx"), (ratio('status=~"4.."'), "4xx")],
         "Share of requests answered 4xx (client) and 5xx (server).", unit="percentunit", decimals=2, no_value=no_http, w=8,
         overrides=[co("5xx", RED), co("4xx", AMBER)], th=steps((None, GREEN), (0.01, RED)))
    b.ts("Latency p50 / p95 / p99", [(quantile(q), f"p{int(q * 100)}") for q in (0.5, 0.95, 0.99)],
         "Server latency percentiles from the histogram. Diamonds are exemplars: click one to open that request's trace in Tempo.",
         unit="s", decimals=3, no_value=no_http, w=8, exemplar=True)
    b.heatmap("Latency Heatmap", f'sum by (le) (increase(http_server_requests_seconds_bucket{{{HTTP}}}[{W}]))',
              "Every request by how long it took, over time: a second band appearing is a slow path (cold cache, lock, "
              "retry) the percentiles average away.", no_value=no_http, w=12)
    b.state("p95 by Route", quantile(0.95, "uri"),
            "p95 latency of each route over time, coloured green < 0.5 s, amber < 1 s, red above: which route got slow, and when.",
            legend="{{uri}}", th=steps((None, GREEN), (0.5, AMBER), (1, RED)), unit="s", no_value=no_http, w=12, h=8)
    b.table("Routes", [
        (rate("http_server_requests_seconds_count", HTTP, "method, uri"), "Req/s"),
        (ratio('status=~"5.."', "method, uri"), "5xx"),
        (ratio('status=~"4.."', "method, uri"), "4xx"),
        (quantile(0.95, "method, uri"), "p95"),
        (quantile(0.99, "method, uri"), "p99")],
        "Per route (method + URI template), current values.", h=8, rename={"method": "Method", "uri": "Route"},
        units=ROUTE_UNITS, sort=("Req/s", True), no_value=no_http)
    b.ts("Error Budget Burn Rate", [
        (f"({ratio('status=~\"5..\"', window='1h')}) / (1 - $slo)", "1h"),
        (f"({ratio('status=~\"5..\"', window='6h')}) / (1 - $slo)", "6h")],
        "Multi-window burn rate against the $slo objective. Both windows above 14.4 = page now; above 6 = ticket; below 1 = within budget.",
        decimals=1, no_value=no_http, w=8, th=steps((None, GREEN), (1, AMBER), (6, ORANGE), (14.4, RED)),
        overrides=[co("1h", RED), co("6h", AMBER)])
    b.ts("Latency Buckets (SLO)", [
        (f'sum(rate(http_server_requests_seconds_bucket{{{HTTP},le=~"0.1|0.25|0.5|1|1.0|2|2.0"}}[{W}])) by (le) / scalar({rate("http_server_requests_seconds_count", HTTP)})', "≤ {{le}}s")],
        "Share of requests faster than each SLO bucket (adhar-kit publishes 50ms…5s SLO buckets).", unit="percentunit", decimals=1,
        no_value=no_http, w=8, mx=1)
    b.ts("In-flight Requests", [(f'sum(http_server_requests_active_seconds_gcount{{{SEL}}})', "active")],
         "Requests being processed right now (Spring's active-request long task timer): saturation of the request path.",
         decimals=0, no_value="No active-request metric (Spring's http.server.requests.active)", w=8)


ROUTE_UNITS = {"Req/s": ("reqps", None, None),
               "5xx": ("percentunit", steps((None, GREEN), (0.001, AMBER), (0.01, RED)), "color-text"),
               "4xx": ("percentunit", steps((None, GREEN), (0.05, AMBER), (0.2, RED)), "color-text"),
               "p95": ("s", steps((None, GREEN), (0.5, AMBER), (1, RED)), "color-text"),
               "p99": ("s", steps((None, GREEN), (1, AMBER), (2, RED)), "color-text")}


def golden_signals_generic(b):
    """RED for any runtime: target A reads Micrometer-named Prometheus metrics,
    target B the OTLP HTTP-server metrics in Mimir, whichever convention the SDK
    used (labels normalised to status / route / method)."""
    no_http = ("No HTTP server metrics: neither http_server_requests_seconds in Prometheus nor OTLP "
               "http.server.* metrics in Mimir for service.name=__APP__")
    b.row("Golden Signals (RED)", collapsed=False)
    b.ts("Requests / s by Status", [(rate("http_server_requests_seconds_count", HTTP, "status"), "{{status}}", PROM),
                                    (otlp_rate("status"), "{{status}}", MIMIR)],
         "Request rate by HTTP status, from Prometheus-scraped or OTLP metrics (probe endpoints excluded).", unit="reqps",
         decimals=2, no_value=no_http, w=8, overrides=[co("200", GREEN), co("500", RED), co("503", RED)])
    b.ts("Error Ratio (4xx / 5xx)", [(ratio('status=~"5.."'), "5xx", PROM), (ratio('status=~"4.."'), "4xx", PROM),
                                     (otlp_ratio("5.."), "5xx", MIMIR), (otlp_ratio("4.."), "4xx", MIMIR)],
         "Share of requests answered 4xx (client) and 5xx (server).", unit="percentunit", decimals=2, no_value=no_http, w=8,
         overrides=[co("5xx", RED), co("4xx", AMBER)], th=steps((None, GREEN), (0.01, RED)))
    b.ts("Latency p50 / p95 / p99", [(quantile(q), f"p{int(q * 100)}", PROM) for q in (0.5, 0.95, 0.99)]
         + [(otlp_quantile(q), f"p{int(q * 100)}", MIMIR) for q in (0.5, 0.95, 0.99)],
         "Server latency percentiles (seconds; millisecond histograms are converted). Diamonds are exemplars where the SDK attaches them.",
         unit="s", decimals=3, no_value=no_http, w=8, exemplar=True)
    b.table("Routes", [
        (rate("http_server_requests_seconds_count", HTTP, "method, uri"), "Req/s", PROM),
        (ratio('status=~"5.."', "method, uri"), "5xx", PROM),
        (quantile(0.95, "method, uri"), "p95", PROM),
        (quantile(0.99, "method, uri"), "p99", PROM)],
        "Per route from Prometheus-scraped metrics (method + URI template), current values.", w=12,
        rename={"method": "Method", "uri": "Route"}, units=ROUTE_UNITS, sort=("Req/s", True),
        no_value="No scraped route metrics; OTLP routes are in the next table")
    b.table("Routes (OTLP)", [
        (otlp_rate("method, route"), "Req/s", MIMIR),
        (otlp_ratio("5..", "method, route"), "5xx", MIMIR),
        (otlp_quantile(0.95, "method, route"), "p95", MIMIR),
        (otlp_quantile(0.99, "method, route"), "p99", MIMIR)],
        "Per route from the OTLP HTTP-server metrics (http.route; old instrumentations report http.target). "
        "Go's otelhttp knows no route, so a Go service shows one row per method.", w=12,
        rename={"method": "Method", "route": "Route"}, units=ROUTE_UNITS, sort=("Req/s", True), ds=MIMIR,
        no_value="No OTLP route metrics; scraped routes are in the previous table",
        hide=["__name__", "service_name", "k8s_namespace_name", "k8s_pod_name"])
    b.state("p95 by Route", f"{quantile(0.95, 'uri')}",
            "p95 latency of each route over time, coloured green < 0.5 s, amber < 1 s, red above (Prometheus-scraped routes).",
            legend="{{uri}}", th=steps((None, GREEN), (0.5, AMBER), (1, RED)), unit="s", no_value="No scraped route metrics", w=12, h=8)
    b.state("p95 by Route (OTLP)", otlp_quantile(0.95, "route"),
            "p95 latency of each OTLP-reported route over time, coloured green < 0.5 s, amber < 1 s, red above.",
            legend="{{route}}", th=steps((None, GREEN), (0.5, AMBER), (1, RED)), unit="s", ds=MIMIR,
            no_value="No OTLP route metrics", w=12, h=8)
    b.ts("Error Budget Burn Rate", [
        (f"({ratio('status=~\"5..\"', window='1h')}) / (1 - $slo)", "1h", PROM),
        (f"({ratio('status=~\"5..\"', window='6h')}) / (1 - $slo)", "6h", PROM),
        (f"({otlp_ratio('5..', window='1h')}) / (1 - $slo)", "1h", MIMIR),
        (f"({otlp_ratio('5..', window='6h')}) / (1 - $slo)", "6h", MIMIR)],
        "Multi-window burn rate against the $slo objective. Both windows above 14.4 = page now; above 6 = ticket; below 1 = within budget.",
        decimals=1, no_value=no_http, w=24, h=6, th=steps((None, GREEN), (1, AMBER), (6, ORANGE), (14.4, RED)),
        overrides=[co("1h", RED), co("6h", AMBER)])


# ---------------------------------------------------------------- dependencies
SPAN = 'service="__APP__"'
SPAN_SERVER = f'{SPAN},span_kind=~"SPAN_KIND_SERVER|SPAN_KIND_CONSUMER",span_name!~"(?i)(http )?get /(actuator|q/health|q/metrics|health|livez|readyz|metrics).*"'
SPAN_OUT = f'{SPAN},span_kind=~"SPAN_KIND_CLIENT|SPAN_KIND_PRODUCER"'
NO_TRACES = "No traces yet: the app exports none to the platform collector (the golden-path templates send OTLP to Alloy)"
NO_GRAPH = ("No calls from this service in Tempo's service graph: it exports no client spans, or calls nothing. "
            "Note: span metrics carry no environment, so this view covers every environment")


def dependencies(b, jvm):
    b.row("Dependencies (what this service calls)")
    b.ts("Downstream Calls / s", [(f'sum by (server) (rate(traces_service_graph_request_total{{client="__APP__"}}[{W}]))', "{{server}}")],
         "Calls per second to each dependency (database, cache, broker, other services) from Tempo's service graph. "
         "All environments together: span metrics carry no environment.", ds=MIMIR, unit="reqps", decimals=2, w=8,
         no_value=NO_GRAPH, legend="table")
    b.ts("Downstream Failures", [
        (f'sum by (server) (rate(traces_service_graph_request_failed_total{{client="__APP__"}}[{W}])) / sum by (server) (rate(traces_service_graph_request_total{{client="__APP__"}}[{W}]))', "{{server}}")],
        "Share of calls to each dependency that failed (span status error).", ds=MIMIR, unit="percentunit", decimals=2, w=8,
        no_value="No failed downstream calls", th=steps((None, GREEN), (0.01, RED)))
    b.ts("Downstream Latency p95", [
        (f'histogram_quantile(0.95, sum by (server, le) (rate(traces_service_graph_request_client_seconds_bucket{{client="__APP__"}}[{W}])))', "{{server}}")],
        "p95 of each dependency as this service waited for it (client side): the dependency that got slow.",
        ds=MIMIR, unit="s", decimals=3, w=8, no_value=NO_GRAPH, legend="table")
    b.table("Dependencies", [
        (f'sum by (server, connection_type) (rate(traces_service_graph_request_total{{client="__APP__"}}[{W}]))', "Calls/s", MIMIR),
        (f'(sum by (server, connection_type) (rate(traces_service_graph_request_failed_total{{client="__APP__"}}[{W}])) or sum by (server, connection_type) (rate(traces_service_graph_request_total{{client="__APP__"}}[{W}])) * 0) / sum by (server, connection_type) (rate(traces_service_graph_request_total{{client="__APP__"}}[{W}]))', "Failed", MIMIR),
        (f'histogram_quantile(0.95, sum by (server, connection_type, le) (rate(traces_service_graph_request_client_seconds_bucket{{client="__APP__"}}[{W}])))', "p95", MIMIR)],
        "Every dependency with call rate, failure share and p95 (connection type: database, messaging_system, virtual_node "
        "for an uninstrumented peer).", w=12, ds=MIMIR, rename={"server": "Dependency", "connection_type": "Type"},
        sort=("Calls/s", True), no_value=NO_GRAPH,
        units={"Calls/s": ("reqps", None, None), "Failed": ("percentunit", steps((None, GREEN), (0.01, AMBER), (0.05, RED)), "color-text"),
               "p95": ("s", steps((None, GREEN), (0.25, AMBER), (1, RED)), "color-text")})
    b.table("Callers", [
        (f'sum by (client) (rate(traces_service_graph_request_total{{server="__APP__"}}[{W}]))', "Calls/s", MIMIR),
        (f'histogram_quantile(0.95, sum by (client, le) (rate(traces_service_graph_request_server_seconds_bucket{{server="__APP__"}}[{W}])))', "p95", MIMIR)],
        "Who calls this service ('user' = traffic from outside the traced services, e.g. the gateway or a browser).",
        w=12, ds=MIMIR, rename={"client": "Caller"}, sort=("Calls/s", True), no_value="No traced callers",
        units={"Calls/s": ("reqps", None, None), "p95": ("s", steps((None, GREEN), (0.5, AMBER), (1, RED)), "color-text")})
    b.ts("Outbound Operations p95", [
        (f'histogram_quantile(0.95, sum by (span_name, le) (rate(traces_spanmetrics_latency_bucket{{{SPAN_OUT}}}[{W}])))', "{{span_name}}")],
        "p95 of each outbound operation (client/producer span): SQL statements, cache commands, HTTP calls, messages sent.",
        ds=MIMIR, unit="s", decimals=3, w=24, legend="table", no_value=NO_TRACES)
    if not jvm:
        return
    db = 'db_client_operation_duration_seconds'
    b.ts("DB Queries / s by Operation", [(f'sum by (db_operation_name, db_collection_name) (rate({db}_count{{{SEL}}}[{W}]))', "{{db_operation_name}} {{db_collection_name}}")],
         "Database operations per second by statement type and table (OpenTelemetry db.client.operation.duration, from adhar-kit's JDBC instrumentation).",
         unit="ops", decimals=2, w=12, legend="table", no_value="No database client metrics in this app")
    b.ts("DB Query Latency p95", [(f'histogram_quantile(0.95, sum by (db_operation_name, db_collection_name, le) (rate({db}_bucket{{{SEL}}}[{W}])))', "{{db_operation_name}} {{db_collection_name}}")],
         "p95 per statement type and table: the slow query, by name.", unit="s", decimals=4, w=12, legend="table",
         no_value="No database client metrics in this app")
    b.ts("DB Connection Pool (Hikari)", [
        (f'sum by (pool) (hikaricp_connections_active{{{SEL}}})', "{{pool}} active"),
        (f'sum by (pool) (hikaricp_connections_idle{{{SEL}}})', "{{pool}} idle"),
        (f'sum by (pool) (hikaricp_connections_pending{{{SEL}}})', "{{pool}} pending"),
        (f'max by (pool) (hikaricp_connections_max{{{SEL}}})', "{{pool}} max")],
        "JDBC pool usage. Pending > 0 means threads are waiting for a connection.", decimals=0, w=12,
        no_value="No JDBC pool in this app")
    b.ts("Connection Acquire Time", [
        (f'sum by (pool) (rate(hikaricp_connections_acquire_seconds_sum{{{SEL}}}[{W}])) / sum by (pool) (rate(hikaricp_connections_acquire_seconds_count{{{SEL}}}[{W}]))', "{{pool}} mean"),
        (f'sum by (pool) (increase(hikaricp_connections_timeout_total{{{SEL}}}[{W}]))', "{{pool}} timeouts")],
        "Mean time to borrow a connection, and acquisition timeouts.", unit="s", decimals=4, w=12, no_value="No JDBC pool in this app")
    b.ts("Kafka Consumer Lag", [(f'max by (topic) (kafka_consumer_fetch_manager_records_lag_max{{{SEL}}})', "{{topic}}")],
         "Max records behind per topic. Growing lag = consumers slower than producers.", decimals=0, w=8,
         no_value="No Kafka consumer in this app")
    b.ts("Kafka Listener", [
        (f'sum by (result) (rate(spring_kafka_listener_seconds_count{{{SEL}}}[{W}]))', "{{result}} /s"),
        (f'sum(rate(spring_kafka_listener_seconds_sum{{{SEL}}}[{W}])) / sum(rate(spring_kafka_listener_seconds_count{{{SEL}}}[{W}]))', "mean time (s)")],
        "@KafkaListener invocations per second by result, and mean processing time.", decimals=3, w=8,
        no_value="No @KafkaListener in this app", overrides=[co("failure /s", RED)])
    b.ts("Cache / Valkey Commands", [
        (f'sum by (command) (rate(lettuce_command_completion_seconds_count{{{SEL}}}[{W}]))', "{{command}} /s")],
        "Valkey (Redis protocol) commands per second through Lettuce (Spring Data Redis).", decimals=1, w=8, legend="table",
        no_value="No Valkey/Redis client metrics in this app")
    b.ts("Outbound HTTP (client)", [
        (f'sum by (client_name, status) (rate(http_client_requests_seconds_count{{{SEL}}}[{W}]))', "{{client_name}} {{status}}"),
        (f'histogram_quantile(0.95, sum by (client_name, le) (rate(http_client_requests_seconds_bucket{{{SEL}}}[{W}])))', "p95 {{client_name}}")],
        "HTTP calls this app makes (RestClient / WebClient), by target host and status, and their p95.", decimals=3, w=24,
        legend="table", no_value="No outbound HTTP client metrics in this app")


# ---------------------------------------------------------------- runtimes
NO_JVM = "Not a JVM app, or JVM metrics are not exported"


def jvm_section(b):
    b.row("JVM")
    b.ts("Heap Used / Committed / Max", [
        (f'sum by (pod) (jvm_memory_used_bytes{{{SEL},area="heap"}})', "used {{pod}}"),
        (f'sum(jvm_memory_committed_bytes{{{SEL},area="heap"}})', "committed"),
        (f'sum(jvm_memory_max_bytes{{{SEL},area="heap"}} > 0)', "max")],
        "Java heap. Used touching max with frequent GC = undersized heap or a leak.", unit="bytes", decimals=1, no_value=NO_JVM,
        overrides=[co("max", RED), co("committed", AMBER)])
    b.ts("Heap After GC", [(f'max by (pod) (jvm_memory_usage_after_gc{{{SEL}}})', "{{pod}}")],
         "Share of the long-lived heap still in use right after a collection. Climbing steadily towards 1 = a leak; "
         "this is the line to watch, not 'used'.", unit="percentunit", decimals=0, no_value=NO_JVM, mx=1,
         th=steps((None, GREEN), (0.7, AMBER), (0.9, RED)))
    b.ts("GC Pause Time", [
        (f'sum by (gc) (rate(jvm_gc_pause_seconds_sum{{{SEL}}}[{W}]))', "{{gc}} (s/s)"),
        (f'max by (gc) (jvm_gc_pause_seconds_max{{{SEL}}})', "{{gc}} max pause")],
        "Seconds spent paused in GC per second, and the longest recent pause.", unit="s", decimals=3, no_value=NO_JVM, w=8)
    b.ts("GC Overhead & Allocation", [
        (f'max by (pod) (jvm_gc_overhead{{{SEL}}})', "overhead {{pod}}"),
        (f'sum(rate(jvm_gc_memory_allocated_bytes_total{{{SEL}}}[{W}])) / 1e9', "allocated GB/s")],
        "Share of CPU time spent in GC, and the allocation rate that drives it.", decimals=3, no_value=NO_JVM, w=8)
    b.ts("Threads", [(f'sum by (state) (jvm_threads_states_threads{{{SEL}}})', "{{state}}")],
         "Live JVM threads by state. Growing BLOCKED/WAITING = contention or a stuck pool.", decimals=0, no_value=NO_JVM, w=8, stack=True)
    b.ts("Non-heap by Pool", [(f'sum by (id) (jvm_memory_used_bytes{{{SEL},area="nonheap"}})', "{{id}}")],
         "Metaspace, code cache and other non-heap pools.", unit="bytes", decimals=1, no_value=NO_JVM, legend="table", w=8)
    b.ts("Direct & Mapped Buffers", [(f'sum by (id) (jvm_buffer_memory_used_bytes{{{SEL}}})', "{{id}}")],
         "Off-heap buffers (Netty, NIO). They count against the container's memory limit but not against -Xmx.",
         unit="bytes", decimals=1, no_value=NO_JVM, w=8)
    b.ts("Classes Loaded & CPU", [
        (f'sum(jvm_classes_loaded_classes{{{SEL}}})', "classes loaded"),
        (f'100 * avg(process_cpu_usage{{{SEL}}})', "process CPU %")],
        "Loaded classes (a steady climb = classloader leak) and the JVM's own CPU view.", decimals=0, no_value=NO_JVM, w=8)
    b.ts("Executor Pools", [
        (f'sum by (name) (executor_active_threads{{{SEL}}})', "{{name}} active"),
        (f'sum by (name) (executor_queued_tasks{{{SEL}}})', "{{name}} queued")],
        "Spring task executors: active threads and queued tasks.", decimals=0, no_value="No executor metrics", w=8)
    b.ts("Log Events by Level (Logback)", [(f'sum by (level) (rate(logback_events_total{{{SEL}}}[{W}]))', "{{level}}")],
         "Log events emitted per second by level, counted inside the JVM.", unit="ops", decimals=2, no_value=NO_JVM, w=8,
         overrides=[co("error", RED), co("warn", AMBER)])
    b.ts("Open Files", [
        (f'sum by (pod) (process_files_open_files{{{SEL}}})', "open {{pod}}"),
        (f'max(process_files_max_files{{{SEL}}})', "max")],
        "File descriptors (sockets included) against the process limit.", decimals=0, no_value=NO_JVM, w=8, overrides=[co("max", RED)])


def business_section(b):
    b.row("Business Metrics (adhar-kit)")
    b.text("Application-specific metrics appear here **automatically**: anything the app publishes under the "
           "**adhar.business.*** name (Prometheus **adhar_business_***) — via **@BusinessMetric**, or "
           "**AdharMetrics** counters, timers and gauges from adhar-kit — gets a panel per metric below, "
           "split by its **category** and **outcome** tags. Nothing to configure on the dashboard.", h=3)
    rep = b.ts("$business_metric", [(f'sum by (category, outcome) (rate({{__name__="$business_metric",{SEL}}}[{W}]))', "{{category}} {{outcome}}")],
               "Per-second rate of this business counter (or timer count), by category and outcome.", unit="ops", decimals=2, w=8,
               no_value="No adhar_business_* metrics from this app yet")
    rep["repeat"] = "business_metric"
    rep["repeatDirection"] = "h"
    rep["maxPerRow"] = 3
    b.table("All Business Series (current)", [(f'{{__name__=~"adhar_business_.+",__name__!~".+_bucket",{SEL}}}', "Value")],
            "Every adhar_business_* series and its current value: counters' totals, gauges, summaries.", h=8,
            hide=["container", "endpoint", "instance", "job", "namespace", "service", "environment", "application"],
            rename={"__name__": "Metric"}, no_value="No adhar_business_* metrics from this app yet")
    b.ts("Persistence (adhar-kit)", [
        (f'sum(rate(adhar_persistence_query_duration_seconds_sum{{{SEL}}}[{W}])) / sum(rate(adhar_persistence_query_duration_seconds_count{{{SEL}}}[{W}]))', "mean query (s)"),
        (f'sum(rate(adhar_persistence_transaction_duration_seconds_sum{{{SEL}}}[{W}])) / sum(rate(adhar_persistence_transaction_duration_seconds_count{{{SEL}}}[{W}]))', "mean transaction (s)"),
        (f'sum(increase(adhar_persistence_slow_queries_total{{{SEL}}}[{W}]))', "slow queries"),
        (f'sum(increase(adhar_persistence_errors_total{{{SEL}}}[{W}]))', "errors")],
        "adhar-kit's persistence layer: mean query and transaction time, slow queries and errors per interval.",
        decimals=4, w=24, h=7, no_value="No adhar_persistence_* metrics (adhar-kit's data module is not in use)",
        overrides=[co("errors", RED), co("slow queries", AMBER)])


NODE_NO = "Not a Node.js app, or it publishes no runtime metrics (prom-client's collectDefaultMetrics / OTel runtime-node)"


def node_runtime(b, title="Node.js Runtime", otlp=False):
    """prom-client default metrics (scraped, the TanStack server) and, for a
    generic backend, OpenTelemetry's runtime-node metrics in Mimir."""
    b.row(title)
    ev = [(f'max by (pod) (nodejs_eventloop_lag_p99_seconds{{{SEL}}})', "p99 {{pod}}", PROM),
          (f'max by (pod) (nodejs_eventloop_lag_mean_seconds{{{SEL}}})', "mean {{pod}}", PROM)]
    heap = [(f'sum by (pod) (nodejs_heap_size_used_bytes{{{SEL}}})', "used {{pod}}", PROM),
            (f'sum by (pod) (nodejs_heap_size_total_bytes{{{SEL}}})', "total {{pod}}", PROM),
            (f'sum by (pod) (process_resident_memory_bytes{{{SEL}}})', "RSS {{pod}}", PROM)]
    if otlp:
        ev.append((f'max by (k8s_pod_name) (nodejs_eventloop_delay_p99_seconds{{{OSEL}}})', "p99 {{k8s_pod_name}} (OTLP)", MIMIR))
        heap.append((f'sum by (k8s_pod_name) (v8js_memory_heap_used_bytes{{{OSEL}}})', "used {{k8s_pod_name}} (OTLP)", MIMIR))
    b.ts("Event Loop Lag", ev,
         "How late the event loop runs callbacks. Every request waits this long on top of its own work; p99 above ~100 ms "
         "is a blocked loop (synchronous work, a huge JSON parse).", unit="s", decimals=3, w=8, no_value=NODE_NO,
         th=steps((None, GREEN), (0.1, RED)))
    b.ts("Heap & RSS", heap, "V8 heap used and total, and the process's resident memory (what the memory limit counts).",
         unit="bytes", decimals=1, w=8, no_value=NODE_NO)
    b.ts("GC Time by Kind", [(f'sum by (kind) (rate(nodejs_gc_duration_seconds_sum{{{SEL}}}[{W}]))', "{{kind}}", PROM)],
         "Seconds per second spent in V8 garbage collection, by kind (minor = scavenge, major = mark-sweep).",
         unit="s", decimals=3, w=8, no_value=NODE_NO)
    b.ts("Active Handles & Requests", [
        (f'sum by (pod) (nodejs_active_handles_total{{{SEL}}})', "handles {{pod}}", PROM),
        (f'sum by (pod) (nodejs_active_requests_total{{{SEL}}})', "requests {{pod}}", PROM)],
        "Open handles (sockets, timers, files) and in-flight libuv requests. A steady climb = a leak.", decimals=0, w=12,
        no_value=NODE_NO)
    b.ts("Process CPU", [(f'sum by (pod) (rate(process_cpu_seconds_total{{{SEL}}}[{W}]))', "{{pod}}", PROM)],
         "CPU seconds per second the Node.js process used. One core (1.0) is the ceiling for the event loop.", decimals=2,
         w=12, no_value=NODE_NO, th=steps((None, GREEN), (0.8, AMBER), (1, RED)))


def generic_runtime(b):
    """Runtime panels for whatever the service is written in: each line exists only
    for its runtime, and container-level signals exist for every one."""
    b.row("Runtime (Go / Python / Node.js / process)")
    b.ts("Runtime Memory", [
        (f'sum by (pod) (process_resident_memory_bytes{{{SEL}}})', "RSS {{pod}}", PROM),
        (f'sum by (pod) (go_memstats_heap_inuse_bytes{{{SEL}}})', "Go heap {{pod}}", PROM),
        (f'sum by (pod) (nodejs_heap_size_used_bytes{{{SEL}}})', "V8 heap {{pod}}", PROM),
        (f'sum by (k8s_pod_name) (go_memory_used_bytes{{{OSEL}}})', "Go memory {{k8s_pod_name}} (OTLP)", MIMIR),
        (f'sum by (k8s_pod_name) (v8js_memory_heap_used_bytes{{{OSEL}}})', "V8 heap {{k8s_pod_name}} (OTLP)", MIMIR),
        (f'sum by (k8s_pod_name) (process_runtime_cpython_memory_bytes{{{OSEL},type="rss"}})', "CPython RSS {{k8s_pod_name}} (OTLP)", MIMIR),
        (f'sum by (k8s_pod_name) (process_memory_usage_bytes{{{OSEL}}})', "process {{k8s_pod_name}} (OTLP)", MIMIR)],
        "The runtime's own memory view: resident set (Prometheus client libraries), Go heap, V8 heap, CPython RSS (OTLP). "
        "Only the lines of this app's runtime appear.", unit="bytes", decimals=1, w=8, legend="table",
        no_value="No runtime memory metrics: the app publishes neither process_* metrics nor OTLP runtime metrics")
    b.ts("Concurrency", [
        (f'sum by (pod) (go_goroutines{{{SEL}}})', "goroutines {{pod}}", PROM),
        (f'sum by (k8s_pod_name) (go_goroutine_count{{{OSEL}}})', "goroutines {{k8s_pod_name}} (OTLP)", MIMIR),
        (f'sum by (pod) (nodejs_active_handles_total{{{SEL}}})', "Node handles {{pod}}", PROM),
        (f'sum by (k8s_pod_name) (process_runtime_cpython_thread_count{{{OSEL}}})', "Python threads {{k8s_pod_name}} (OTLP)", MIMIR),
        (f'sum by (pod) (container_threads{{{SEL},{APP_CONTAINER}}})', "OS threads {{pod}}", PROM)],
        "Goroutines (Go), active handles (Node.js), interpreter threads (Python) and the container's OS threads. A line that "
        "only ever climbs is a leak.", decimals=0, w=8, legend="table", no_value="No pods")
    b.ts("Garbage Collection", [
        (f'sum by (pod) (rate(go_gc_duration_seconds_sum{{{SEL}}}[{W}]))', "Go GC s/s {{pod}}", PROM),
        (f'sum by (pod) (rate(nodejs_gc_duration_seconds_sum{{{SEL}}}[{W}]))', "V8 GC s/s {{pod}}", PROM),
        (f'sum by (generation) (rate(python_gc_collections_total{{{SEL}}}[{W}]))', "Python gen{{generation}} /s", PROM),
        (f'sum by (k8s_pod_name) (rate(process_runtime_cpython_gc_count_total{{{OSEL}}}[{W}]))', "Python GC /s {{k8s_pod_name}} (OTLP)", MIMIR)],
        "Time (Go, V8) or collections (Python) spent in garbage collection.", decimals=3, w=8, legend="table",
        no_value="No GC metrics for this runtime")
    b.ts("Event Loop Lag (Node.js)", [
        (f'max by (pod) (nodejs_eventloop_lag_p99_seconds{{{SEL}}})', "p99 {{pod}}", PROM),
        (f'max by (k8s_pod_name) (nodejs_eventloop_delay_p99_seconds{{{OSEL}}})', "p99 {{k8s_pod_name}} (OTLP)", MIMIR)],
        "How late Node.js runs callbacks; p99 above ~100 ms is a blocked loop.", unit="s", decimals=3, w=8, no_value=NODE_NO,
        th=steps((None, GREEN), (0.1, RED)))
    b.ts("Open File Descriptors & Sockets", [
        (f'sum by (pod) (process_open_fds{{{SEL}}})', "fds {{pod}}", PROM),
        (f'max(process_max_fds{{{SEL}}})', "fd limit", PROM),
        (f'sum by (pod) (container_sockets{{{SEL},{APP_CONTAINER}}})', "sockets {{pod}}", PROM)],
        "Open file descriptors (Prometheus client libraries) and the container's sockets: a connection leak climbs here first.",
        decimals=0, w=8, no_value="No pods", overrides=[co("fd limit", RED)])
    b.ts("Pressure Stall (PSI)", [
        (f'sum by (pod) (rate(container_pressure_cpu_waiting_seconds_total{{{SEL},{APP_CONTAINER}}}[{W}]))', "cpu {{pod}}", PROM),
        (f'sum by (pod) (rate(container_pressure_memory_waiting_seconds_total{{{SEL},{APP_CONTAINER}}}[{W}]))', "memory {{pod}}", PROM),
        (f'sum by (pod) (rate(container_pressure_io_waiting_seconds_total{{{SEL},{APP_CONTAINER}}}[{W}]))', "io {{pod}}", PROM)],
        "Share of time the app's tasks waited for CPU, memory or I/O (Linux PSI): the runtime-independent answer to 'is it "
        "starved?'. Above ~0.1 the app is noticeably slowed.", unit="percentunit", decimals=2, w=8, legend="table",
        no_value="No pods", th=steps((None, GREEN), (0.1, AMBER), (0.25, RED)))


# ---------------------------------------------------------------- shared
def saturation(b, collapsed=True):
    b.row("Saturation & Resources", collapsed=collapsed)
    b.ts("CPU vs Request / Limit", [
        (f'sum by (pod) (rate(container_cpu_usage_seconds_total{{{SEL},{APP_CONTAINER}}}[{W}]))', "{{pod}}"),
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
    b.ts("CPU Throttling", [(f'100 * sum by (pod) (rate(container_cpu_cfs_throttled_periods_total{{{SEL},{APP_CONTAINER}}}[{W}])) / clamp_min(sum by (pod) (rate(container_cpu_cfs_periods_total{{{SEL},{APP_CONTAINER}}}[{W}])), 1)', "{{pod}}")],
         "Share of CFS periods throttled. Sustained throttling inflates latency long before CPU looks full.", unit="percent", decimals=1, w=8)
    b.ts("Restarts", [(f'sum by (pod) (increase(kube_pod_container_status_restarts_total{{{SEL},{APP_CONTAINER}}}[{W}])) > 0', "{{pod}}")],
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
        (f'sum(rate(container_network_receive_bytes_total{{{SEL}}}[{W}]))', "receive"),
        (f'-sum(rate(container_network_transmit_bytes_total{{{SEL}}}[{W}]))', "transmit")],
        "Pod network throughput (transmit drawn below zero).", unit="Bps", decimals=1, h=6)


def logs_section(b, kind):
    b.row("Logs (Loki)")
    cnt = lambda r: f"count_over_time({r})"
    errors = ' | detected_level=~"error|fatal|critical"'
    b.ts("Log Lines by Level", [(f'sum by (detected_level) {logq(cnt)}', "{{detected_level}}")],
         "Lines per interval by the level Loki detected in the JSON/logfmt 'level' field, for the components picked in "
         "'Log components' (the app by default; add db / cache / events for its database, cache and Kafka).",
         ds=LOKI, decimals=0, draw="bars", stack=True,
         overrides=[co("error", RED), co("fatal", RED), co("critical", RED), co("warn", AMBER), co("info", GREEN)], w=12, no_value="No logs")
    b.ts("Error Lines by Component & Pod", [(f'sum by (adhar_component, pod) {logq(cnt, errors)}', "{{adhar_component}} {{pod}}")],
         "Error-level log lines per interval, per component and pod: one pod erroring alone points at that node or that "
         "pod's state; errors in db/cache/events point below the app.", ds=LOKI, decimals=0, draw="bars", stack=True, w=12,
         no_value="No error lines")
    if kind == "backend-jvm":
        b.table("Top Error Loggers", [(f'topk(10, sum by (logger) {logq(cnt, " | detected_level=~\"error|fatal|warn\" | json logger | __error__=\"\"", "$__range", sel=APP_ONLY)})', "Lines")],
                "The classes logging the most warnings and errors (adhar-kit's JSON 'logger' field).", w=12, ds=LOKI,
                rename={"logger": "Logger"}, sort=("Lines", True), no_value="No warnings or errors")
        b.table("Top Error Messages", [(f'topk(10, sum by (message) {logq(cnt, errors + " | json message | __error__=\"\"", "$__range", sel=APP_ONLY)})', "Lines")],
                "The most frequent error messages (JSON 'message' field).", w=12, ds=LOKI, rename={"message": "Message"},
                sort=("Lines", True), no_value="No error lines")
    p = b.logs("Logs", f'{APP_LOGS} |~ "(?i)$search"',
               "The log lines of the components picked in 'Log components' (search with the 'Log search' box, a "
               "case-insensitive regex). A traceId field links to the trace in Tempo. Every application's logs side by "
               "side: the Application Logs dashboard (link above).")
    p["targets"].append({"datasource": LOKI, "refId": "B", "expr": f'{APP_LOGS_LEGACY} |~ "(?i)$search"', "queryType": "range"})


def traces_section(b, kind):
    b.row("Traces (Tempo)")
    if kind != "frontend":
        b.ts("Traced Requests / s", [(f'sum by (status_code) (rate(traces_spanmetrics_calls_total{{{SPAN_SERVER}}}[{W}]))', "{{status_code}}")],
             "Server/consumer spans per second from Tempo's span metrics (in Mimir), by span status, probes excluded. "
             "All environments together: span metrics carry no environment.", ds=MIMIR, unit="reqps", decimals=2, w=8,
             no_value=NO_TRACES, overrides=[co("STATUS_CODE_ERROR", RED), co("STATUS_CODE_OK", GREEN)])
        b.ts("Traced Error Ratio", [(f'(sum(rate(traces_spanmetrics_calls_total{{{SPAN_SERVER},status_code="STATUS_CODE_ERROR"}}[{W}])) or sum(rate(traces_spanmetrics_calls_total{{{SPAN_SERVER}}}[{W}])) * 0) / sum(rate(traces_spanmetrics_calls_total{{{SPAN_SERVER}}}[{W}]))', "error ratio")],
             "Share of server spans with error status.", ds=MIMIR, unit="percentunit", decimals=2, w=8, no_value=NO_TRACES,
             overrides=[co("error ratio", RED)])
        b.ts("Traced Latency p95 by Operation", [(f'histogram_quantile(0.95, sum by (span_name, le) (rate(traces_spanmetrics_latency_bucket{{{SPAN_SERVER}}}[{W}])))', "{{span_name}}")],
             "p95 span duration per operation.", ds=MIMIR, unit="s", decimals=3, w=8, legend="table", no_value=NO_TRACES)
        b.traces("Recent Traces", '{resource.service.name="__APP__" && kind=server}', "Latest server traces of this service. Click a trace ID to open it.")
        b.traces("Error Traces", '{resource.service.name="__APP__" && status=error}', "Latest traces containing an error span.",
                 no_value="No error traces")
        b.traces("Slow Traces (> 1s)", '{resource.service.name="__APP__" && kind=server && duration > 1s}',
                 "Latest server traces slower than one second.", no_value="No trace slower than one second")
        b.traces("Slow Database / Client Calls (> 250ms)", '{resource.service.name="__APP__" && kind=client && duration > 250ms}',
                 "Latest outbound calls (SQL, cache, HTTP) slower than 250 ms, with the trace they belong to.",
                 no_value="No client call slower than 250 ms")
    else:
        b.traces("Recent Browser Traces", '{resource.service.name="__APP__"}',
                 "Latest traces started in the browser (document load, fetch/XHR). Click a trace ID to open it.", w=12,
                 no_value="No browser traces yet: the page reports none (Faro's tracing instrumentation sends them)")
        b.traces("Browser Traces with Errors", '{resource.service.name="__APP__" && status=error}',
                 "Latest browser traces containing an error span.", w=12, no_value="No browser trace with an error")


# ---------------------------------------------------------------- per type
def panels(kind):
    b = Board()
    overview(b, kind)
    if kind == "backend-jvm":
        golden_signals_scraped(b)
        dependencies(b, jvm=True)
        jvm_section(b)
        business_section(b)
    elif kind == "backend-generic":
        golden_signals_generic(b)
        dependencies(b, jvm=False)
        generic_runtime(b)
    elif kind == "frontend":
        frontend(b)
    elif kind == "fullstack":
        frontend(b, server_side=True)
        golden_signals_scraped(b, title="Server: Golden Signals (RED)", collapsed=True,
                               no_http="No server metrics: the TanStack server publishes http_server_requests_seconds on its metrics port (src/server/metrics.mjs, PodMonitor)")
        node_runtime(b, title="Server: Node.js Runtime")
        dependencies(b, jvm=False)
    saturation(b)
    logs_section(b, kind)
    traces_section(b, kind)
    return b.panels


DESCRIPTION = {
    "backend-jvm": "golden signals, dependencies, JVM, business metrics, resources, logs, traces, delivery",
    "backend-generic": "golden signals (Prometheus or OTLP), dependencies, runtime, resources, logs, traces, delivery",
    "frontend": "real-user monitoring (Core Web Vitals, JS errors, browser network, pages, releases), what nginx served, resources, logs, traces, delivery",
    "fullstack": "real-user monitoring, the Node.js server's golden signals and runtime, dependencies, resources, logs, traces, delivery",
}


def dashboard(kind):
    variables = ["__ENV_VAR__",
                 {"name": "pod", "label": "Pod", "type": "query", "datasource": PROM,
                  "definition": 'label_values(kube_pod_info{namespace="$env",pod=~"__POD_RE__"}, pod)',
                  "query": {"qryType": 1, "query": 'label_values(kube_pod_info{namespace="$env",pod=~"__POD_RE__"}, pod)', "refId": "var-pod"},
                  "includeAll": True, "multi": True, "allValue": "__POD_RE__", "refresh": 2, "sort": 1,
                  "current": {"text": ["All"], "value": ["$__all"]}, "options": [], "hide": 0}]
    if kind == "backend-jvm":
        variables.append(
            {"name": "business_metric", "label": "Business metric", "type": "query", "datasource": PROM,
             "definition": 'label_values({__name__=~"adhar_business_.+_(total|count)",namespace="$env",pod=~"$pod"}, __name__)',
             "query": {"qryType": 1, "query": 'label_values({__name__=~"adhar_business_.+_(total|count)",namespace="$env",pod=~"$pod"}, __name__)', "refId": "var-business_metric"},
             "includeAll": True, "multi": True, "refresh": 2, "sort": 1, "hide": 2,
             "current": {"text": ["All"], "value": ["$__all"]}, "options": []})
    if kind != "frontend":
        variables.append(
            {"name": "slo", "label": "Availability SLO", "type": "custom", "query": "0.99,0.995,0.999",
             "current": {"text": "0.995", "value": "0.995"}, "includeAll": False, "multi": False, "hide": 0,
             "options": [{"text": v, "value": v, "selected": v == "0.995"} for v in ("0.99", "0.995", "0.999")]})
    variables.append({"name": "component", "label": "Log components", "type": "custom", "query": "app,db,cache,events",
                      "multi": True, "includeAll": True, "allValue": ".+", "hide": 0,
                      "current": {"text": ["app"], "value": ["app"]},
                      "options": [{"text": v, "value": v, "selected": v == "app"} for v in ("app", "db", "cache", "events")]})
    variables.append({"name": "search", "label": "Log search", "type": "textbox", "query": "", "current": {"text": "", "value": ""}, "hide": 0})
    return {
        "uid": "app-__APP__",
        "title": "__APP__ · application",
        "description": f"{kind} dashboard of the custom application __APP__, composed by the Application composition "
                       f"({DESCRIPTION[kind]}). Generated by hack/dashboards/build-app-dashboard.py.",
        "tags": ["adhar", "application", "__APP__", kind],
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
        "templating": {"list": variables},
        "panels": panels(kind),
    }


# Five links at most (with the host known), so an application's controls stay
# on one row beside its variables. Console and Adhar Platform were dropped: the
# console is not about THIS application, and the platform front page is one
# click away in Grafana's home and in the "Applications" dropdown's breadcrumb.
LINKS_KNOWN_HOST = [
    {"title": "Argo CD", "type": "link", "icon": "external link", "targetBlank": True, "includeVars": False, "keepTime": False,
     "url": "https://argocd.__PUBLIC_HOST__/applications/adhar-system/__ARGO_APP__${env:text}", "tooltip": "This environment's Argo CD Application"},
    {"title": "Kargo", "type": "link", "icon": "external link", "targetBlank": True, "includeVars": False, "keepTime": False,
     "url": "https://kargo.__PUBLIC_HOST__/project/__PROJECT__", "tooltip": "Promotion pipeline (dev → test → prod)"},
    {"title": "Repository", "type": "link", "icon": "external link", "targetBlank": True, "includeVars": False, "keepTime": False,
     "url": "__REPO_WEB__", "tooltip": "Source repository in Gitea"},
]
LINKS_ALWAYS = [
    {"title": "Application Logs", "type": "link", "icon": "doc", "targetBlank": False, "includeVars": False, "keepTime": False,
     "url": "/d/adhar-app-logs?var-application=__APP__&var-environment=${env:text}&${__url_time_range}",
     "tooltip": "This application's logs (app, db, cache, events) beside every other application's"},
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


def snippet():
    """The Go-template block placed in the composition between the markers."""
    blocks = [(TEMPLATE_VAR[k], compact(dashboard(k))) for k in TYPES]
    blocks += [("$appLinksHost", compact(LINKS_KNOWN_HOST + LINKS_ALWAYS)), ("$appLinks", compact(LINKS_ALWAYS))]
    for _, s in blocks:
        if "`" in s:
            sys.exit("a backtick in the dashboard JSON would end the Go raw string")
    ind = " " * 12

    def raw(var, s):
        lines = s.splitlines()
        return f"{ind}{{{{- {var} := `" + lines[0] + "\n" + "\n".join(ind + l for l in lines[1:]) + "` -}}\n"

    return f"{ind}{BEGIN}\n" + "".join(raw(v, s) for v, s in blocks) + f"{ind}{END}\n"


def main():
    if "--dump" in sys.argv:
        out = sys.argv[sys.argv.index("--dump") + 1]
        os.makedirs(out, exist_ok=True)
        for k in TYPES:
            with open(os.path.join(out, f"{k}.json"), "w") as f:
                json.dump(dashboard(k), f, indent=1, ensure_ascii=False)
        return
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
