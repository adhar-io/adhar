"""Applications folder: the ONE shipped dashboard filed there, Application Logs.

Every other dashboard in Applications is composed per custom application
(app-<name>, by the Application composition). This one is the cross-application
view of their LOGS: pick an environment, an application and a component and read
everything they wrote, without knowing a single pod name.

It keys on stream labels the Alloy pod-log pipeline derives for pods in namespaces
labelled adhar.io/plane=workload ONLY (observability/alloy/values.yaml):

  adhar_application  the owning app: pod label adhar.io/application, else
                     app.kubernetes.io/instance / app with a trailing
                     -db | -cache | -events stripped
  adhar_component    app | db | cache | events (or the pod's adhar.io/component)
  adhar_environment  the namespace's adhar.io/environment
  adhar_team, adhar_project   the namespace's adhar.io/team, adhar.io/project

Platform streams carry none of them, so `All` (= .+) never drags adhar-system in.
"""
import json
import urllib.parse

from dashlib import *  # noqa: F401,F403
from dash_common import UIDS, links, tags

ERR = 'detected_level=~"error|fatal|critical"'
WARN = 'detected_level=~"warn|warning"'
# The selected applications' streams. All = .+ (not .*): Loki refuses a selector whose
# matchers all match the empty string, and .+ also keeps platform streams out.
SEL = 'adhar_environment=~"$environment", adhar_application=~"$application", adhar_component=~"$component"'
FILTERS = '| detected_level=~"$level" |~ "(?i)$search"'

# The first readable sentence of an error line: the JSON message/msg field when the
# line is JSON (adhar-kit apps, CNPG), else the raw line; ids and numbers folded to N
# so one message counted a thousand times is one row, not a thousand.
MESSAGE = ('| json message="message", msg="msg" | drop __error__, __error_details__ '
           '| label_format message=`{{ regexReplaceAll "[0-9a-fA-F-]{8,}|[0-9]+" '
           '(default (default __line__ .msg) .message) "N" | trunc 110 }}`')

LEVEL_COLORS = [ov_color("^(error|fatal|critical)$", RED), ov_color("^warn(ing)?$", AMBER),
                ov_color("^info$", GREEN), ov_color("^debug|trace$", BLUE), ov_color("^unknown$", GREY)]


def L(expr, legend="", instant=False):
    """A Loki query (instant queries carry queryType so Grafana does not guess)."""
    t = T(expr, legend, instant=instant, ds=LOKI)
    t["queryType"] = "instant" if instant else "range"
    return t


def on_loki(panel):
    panel["datasource"] = LOKI
    return panel


def ltable(title, desc, targets, columns, **kw):
    """A table over Loki instant queries. Loki answers a metric query with one frame
    per series and the labels ON its Value field — not the label columns a
    Prometheus `format: table` query gets — so the frames are flattened with
    labelsToFields, then merged into one row per label tuple; each query's value
    becomes "Value #<refId>" (verified on Grafana 13.2 against Loki 3.6: without
    labelsToFields the merge yields no rows, joinByLabels errors)."""
    p = on_loki(table(title, desc, targets, columns, **kw))
    p["transformations"] = [{"id": "labelsToFields", "options": {"mode": "columns"}},
                            {"id": "merge", "options": {}}, p["transformations"][-1]]
    return p


def lvar(name, label, lbl, stream, desc):
    """A multi-select Loki label_values variable; All = .+."""
    return {"name": name, "label": label, "type": "query", "datasource": LOKI, "description": desc,
            "definition": f"label_values({stream}, {lbl})",
            "query": {"type": 1, "label": lbl, "stream": stream, "refId": "LokiVariableQueryEditor-VariableQuery"},
            "refresh": 2, "regex": "", "sort": 1, "hide": 0, "multi": True, "includeAll": True,
            "allValue": ".+", "options": [], "current": {"selected": True, "text": ["All"], "value": ["$__all"]}}


def level_var():
    levels = ["error", "warn", "info", "debug", "unknown"]
    return {"name": "level", "label": "Level", "type": "custom", "query": ",".join(levels),
            "description": "Loki's detected_level (read from the JSON/logfmt level field; 'unknown' when a line has none).",
            "multi": True, "includeAll": True, "allValue": ".*", "hide": 0,
            "current": {"selected": True, "text": ["All"], "value": ["$__all"]},
            "options": [{"selected": False, "text": v, "value": v} for v in levels]}


def search_var():
    return {"name": "search", "label": "Search", "type": "textbox", "query": "", "hide": 0,
            "description": "Case-insensitive text (a regular expression) the line must contain.",
            "current": {"selected": False, "text": "", "value": ""}, "options": []}


def explore_url(expr):
    """Grafana Explore on Loki, pre-filled with `expr`. ${...} stays unencoded so
    Grafana interpolates data-link fields and variables into it."""
    panes = {"a": {"datasource": "loki", "queries": [{"refId": "A", "expr": expr, "queryType": "range",
                                                       "datasource": {"type": "loki", "uid": "loki"}}],
                   "range": {"from": "${__from}", "to": "${__to}"}}}
    enc = urllib.parse.quote(json.dumps(panes, separators=(",", ":")), safe="")
    # Put the Grafana template expressions back as written.
    for raw in ("${__from}", "${__to}", "${__data.fields.adhar_application}", "${__data.fields.adhar_environment}"):
        enc = enc.replace(urllib.parse.quote(raw, safe=""), raw)
    return f"/explore?schemaVersion=1&orgId=1&panes={enc}"


APP = "${__data.fields.adhar_application}"
ENV = "${__data.fields.adhar_environment}"
APP_LINKS = [
    {"title": "Open the application dashboard", "targetBlank": False,
     "url": f"/d/app-{APP}?var-env={ENV}&${{__url_time_range}}"},
    {"title": "Explore these logs",  "targetBlank": False,
     "url": explore_url(f'{{adhar_application="{APP}", adhar_environment="{ENV}"}}')},
    {"title": "Open in Logs Drilldown", "targetBlank": False,
     "url": f"/a/grafana-lokiexplore-app/explore/adhar_application/{APP}/logs?var-ds=loki&${{__url_time_range}}"},
]


def app_logs():
    d = Dash(UIDS["app-logs"], "Application Logs",
             "Logs of every custom application, by environment, application and component (the app itself, its "
             "database, cache and event stream), without knowing a pod name. Labels come from the Alloy pod-log "
             "pipeline, which tags streams in workload namespaces only (adhar_application / adhar_component / "
             "adhar_environment), so platform logs never mix in. Each application links to its own dashboard, "
             "to Explore and to Logs Drilldown.",
             tags("Applications", "logs", "loki"),
             [lvar("environment", "Environment", "adhar_environment", '{adhar_environment=~".+"}',
                   "Environment namespaces of the custom applications (dev, test, prod, ...)."),
              lvar("application", "Application", "adhar_application", '{adhar_environment=~"$environment"}',
                   "Custom applications that logged in the selected environments."),
              lvar("component", "Component", "adhar_component",
                   '{adhar_environment=~"$environment", adhar_application=~"$application"}',
                   "app = the application's own pods; db / cache / events = the backing services the platform "
                   "composed for it."),
              level_var(), search_var()],
             links=links("Applications") + [
                 {"title": "Logs Drilldown", "type": "link", "url": "/a/grafana-lokiexplore-app/explore",
                  "asDropdown": False, "includeVars": False, "keepTime": True, "targetBlank": False,
                  "icon": "external link", "tags": [], "tooltip": "Grafana's queryless log browser"}],
             time_from="now-1h", collapse={"Application logs at a glance", "Logs"})

    d.row("Application logs at a glance")
    d.line(4,
           (on_loki(stat("Errors (15m)", "Error, fatal and critical lines from the selected applications in the "
                         "last 15 minutes (Loki's detected_level).",
                         [L(f'sum(count_over_time({{{SEL}}} | {ERR} [15m]))', instant=True)],
                         th=steps((GREEN, None), (AMBER, 1), (RED, 50)), decimals=0, no_value="0")), 4),
           (on_loki(stat("Apps with errors (15m)", "Applications (in any selected environment) that logged at least "
                         "one error in the last 15 minutes.",
                         [L(f'count(sum by (adhar_application) (count_over_time({{{SEL}}} | {ERR} [15m])))', instant=True)],
                         th=steps((GREEN, None), (AMBER, 1), (RED, 3)), decimals=0, no_value="0")), 4),
           (on_loki(stat("Error share (15m)", "Errors as a share of all lines in the last 15 minutes. Above 5% the "
                         "logs are mostly bad news.",
                         [L(f'sum(count_over_time({{{SEL}}} | {ERR} [15m])) / sum(count_over_time({{{SEL}}}[15m]))',
                            instant=True)],
                         unit="percentunit", th=steps((GREEN, None), (AMBER, 0.01), (RED, 0.05)), decimals=2,
                         no_value="0")), 4),
           (on_loki(stat("Warnings (15m)", "Warning lines in the last 15 minutes.",
                         [L(f'sum(count_over_time({{{SEL}}} | {WARN} [15m]))', instant=True)],
                         th=steps((GREEN, None), (AMBER, 50), (RED, 500)), decimals=0, no_value="0")), 4),
           (on_loki(stat("Log rate", "Lines per second from the selected applications (5-minute average).",
                         [L(f'sum(rate({{{SEL}}}[5m]))', instant=True)],
                         unit="short", decimals=1, no_value="0")), 4),
           (on_loki(stat("Applications logging", "Applications that wrote at least one line in the last 15 minutes. "
                         "An application missing here is silent, not necessarily healthy.",
                         [L(f'count(sum by (adhar_application) (count_over_time({{{SEL}}}[15m])))', instant=True)],
                         decimals=0, no_value="0")), 4))
    d.line(8,
           (on_loki(ts("Log volume by level", "Lines per interval by detected level, after the level and search "
                       "filters. A red band growing is the first thing to look at.",
                       [L(f'sum by (detected_level) (count_over_time({{{SEL}}} {FILTERS} [$__auto]))', "{{detected_level}}")],
                       draw="bars", stack=True, decimals=0, overrides=LEVEL_COLORS, calcs=("sum", "max"))), 12),
           (on_loki(ts("Log volume by application", "Lines per interval by application (all its components), after the "
                       "level and search filters.",
                       [L(f'sum by (adhar_application) (count_over_time({{{SEL}}} {FILTERS} [$__auto]))',
                          "{{adhar_application}}")],
                       draw="bars", stack=True, decimals=0, calcs=("sum", "max"))), 12))
    d.line(9,
           (on_loki(ts("Error rate by application", "Error lines per second, per application and environment. A step "
                       "up right after a promotion points at the release.",
                       [L(f'sum by (adhar_application, adhar_environment) (rate({{{SEL}}} | {ERR} [$__auto]))',
                          "{{adhar_application}} · {{adhar_environment}}")],
                       decimals=3, calcs=("mean", "max", "lastNotNull"))), 12),
           (ltable("Top error messages", "The most frequent error messages in the range: the JSON message/msg "
                          "field when the line is JSON, else the line itself, with ids and numbers folded to N so "
                          "repeats group together.",
                          [L(f'topk(15, sum by (adhar_application, adhar_environment, message) '
                             f'(count_over_time({{{SEL}}} | {ERR} {MESSAGE} [$__range])))', instant=True)],
                          [("adhar_application", "Application", None, {"width": 170, "links": APP_LINKS}),
                           ("adhar_environment", "Env", None, {"width": 70}),
                           ("message", "Message", None, None),
                           ("Value #A", "Count", "short", {"cell": "color-background", "width": 80,
                                                        "th": steps((AMBER, None), (RED, 100))})],
                          sort=("Count", True)), 12))
    d.line(8,
           (ltable("Applications", "Every application with logs in the range, per environment: lines, errors "
                   "and warnings. Click an application for its dashboard, Explore or Logs Drilldown.",
                   [L(f'sum by (adhar_application, adhar_environment) (count_over_time({{{SEL}}}[$__range]))', instant=True),
                    L(f'sum by (adhar_application, adhar_environment) (count_over_time({{{SEL}}} | {ERR} [$__range]))', instant=True),
                    L(f'sum by (adhar_application, adhar_environment) (count_over_time({{{SEL}}} | {WARN} [$__range]))', instant=True)],
                   [("adhar_application", "Application", None, {"links": APP_LINKS}),
                    ("adhar_environment", "Environment", None, None),
                    ("Value #A", "Lines", "short", None),
                    ("Value #B", "Errors", "short", {"cell": "color-background", "novalue": "0",
                                                   "th": steps((GREEN, None), (AMBER, 1), (RED, 50))}),
                    ("Value #C", "Warnings", "short", {"cell": "color-text", "novalue": "0",
                                                       "th": steps((GREEN, None), (AMBER, 1))})],
                   sort=("Errors", True)), 24))

    d.row("Logs")
    lg = logs("Logs", "Every line from the selected applications, environments and components, newest first, after "
              "the level and search filters. JSON lines are pretty-printed; expand a line for its labels "
              "(application, component, pod).",
              f'{{{SEL}}} {FILTERS}')
    lg["targets"][0]["queryType"] = "range"
    lg["options"].update({"prettifyLogMessage": True, "showLabels": False, "showCommonLabels": False,
                          "wrapLogMessage": True, "sortOrder": "Descending"})
    d.line(20, (lg, 24))

    d.row("Environments & components")
    d.line(8,
           (on_loki(ts("Log volume by environment", "Lines per interval by environment.",
                       [L(f'sum by (adhar_environment) (count_over_time({{{SEL}}} {FILTERS} [$__auto]))',
                          "{{adhar_environment}}")],
                       draw="bars", stack=True, decimals=0, calcs=("sum", "max"))), 8),
           (on_loki(ts("Log volume by component", "Lines per interval by component: the app's own pods (app) and the "
                       "database, cache and event stream composed for it.",
                       [L(f'sum by (adhar_component) (count_over_time({{{SEL}}} {FILTERS} [$__auto]))',
                          "{{adhar_component}}")],
                       draw="bars", stack=True, decimals=0, calcs=("sum", "max"))), 8),
           (ltable("Noisiest pods", "The pods that wrote the most lines in the range — a chatty logger costs "
                          "Loki storage and hides the lines that matter.",
                          [L(f'topk(15, sum by (adhar_application, adhar_environment, adhar_component, pod) '
                             f'(count_over_time({{{SEL}}}[$__range])))', instant=True)],
                          [("pod", "Pod", None, None), ("adhar_component", "Component", None, {"width": 90}),
                           ("adhar_environment", "Env", None, {"width": 60}),
                           ("Value #A", "Lines", "short", {"width": 80})],
                          sort=("Lines", True), hide=("adhar_application",)), 8))

    d.row("Backing services (database, cache, events)")
    d.line(8,
           (on_loki(ts("Backing-service errors", "Error lines per second from the database, cache and event stream "
                       "the platform composed for each application.",
                       [L(f'sum by (adhar_application, adhar_component, adhar_environment) '
                          f'(rate({{{SEL}, adhar_component!="app"}} | {ERR} [$__auto]))',
                          "{{adhar_application}} {{adhar_component}} · {{adhar_environment}}")],
                       decimals=3)), 24))
    bl = logs("Backing-service warnings & errors", "Warning and error lines from the selected applications' database, "
              "cache and event stream, newest first.",
              f'{{{SEL}, adhar_component!="app"}} | detected_level=~"error|fatal|critical|warn|warning" |~ "(?i)$search"')
    bl["targets"][0]["queryType"] = "range"
    d.line(10, (bl, 24))
    return d


DASHBOARDS = [
    ("app-logs", "Applications", app_logs),
]
