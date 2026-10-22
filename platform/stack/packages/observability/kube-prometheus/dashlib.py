"""Shared building blocks for the hand-authored Adhar Grafana dashboards built by
build-service-dashboards.py (backing services + Kubernetes).

One visual language for every dashboard built here:
  * a row of colour-coded stat tiles at the top answers "is it healthy?" at a
    glance (background colour = state, so red is visible from across the room);
  * time series use smooth lines, a light gradient fill, and a TABLE legend with
    mean / max / last so the numbers can be read without hovering;
  * ratios are gauges or bar gauges with the same green / amber / red bands;
  * state timelines for anything that has a state (role, up/down);
  * top-N tables with coloured cells and a link to the pod's resource dashboard;
  * every panel carries a description (the (i) tooltip) saying what it shows
    AND what a bad value means;
  * every line on the 24-column grid is filled exactly — `Dash.line` refuses a
    line whose widths do not add up to 24, so there are no gaps and no overlaps.

Datasources: Prometheus panels use the `${datasource}` template variable (whose
default is the provisioned `prometheus` UID); Loki panels use the provisioned
`loki` UID directly.
"""

PROM = {"type": "prometheus", "uid": "${datasource}"}
LOKI = {"type": "loki", "uid": "loki"}

GREEN, AMBER, RED, BLUE, GREY = "green", "#EAB839", "red", "blue", "text"
PURPLE = "purple"


def steps(*pairs):
    """steps(("green", None), ("orange", 70), ("red", 90))"""
    return {"mode": "absolute", "steps": [{"color": c, "value": v} for c, v in pairs]}


OK_BAD = steps((GREEN, None), (RED, 1))                 # 0 good, >=1 bad
OK_WARN_BAD = steps((GREEN, None), (AMBER, 1), (RED, 5))
PCT = steps((GREEN, None), (AMBER, 75), (RED, 90))       # utilisation bands
PCT_REQ = steps((GREEN, None), (AMBER, 85), (RED, 95))   # request-commitment bands
NEUTRAL = steps((BLUE, None))
GOOD_HIGH = steps((RED, None), (AMBER, 80), (GREEN, 95))  # hit ratios etc.


def vmap(*entries):
    """Value mappings: vmap((0, "Down", "red"), (1, "Up", "green"))."""
    opts = {}
    for i, (value, text, color) in enumerate(entries):
        opts[str(value)] = {"text": text, "color": color, "index": i}
    return [{"type": "value", "options": opts}]


def rmap(*entries):
    """Range mappings: rmap((from, to, text, color), ...)."""
    out = []
    for i, (lo, hi, text, color) in enumerate(entries):
        out.append({"type": "range", "options": {"from": lo, "to": hi,
                                                   "result": {"text": text, "color": color, "index": i}}})
    return out


def T(expr, legend="", instant=False, fmt=None, interval=None, ds=None):
    t = {"datasource": ds or PROM, "expr": expr, "legendFormat": legend,
         "range": not instant, "instant": instant, "editorMode": "code"}
    if fmt:
        t["format"] = fmt
    if interval:
        t["interval"] = interval
    return t


def _targets(targets):
    out = []
    for i, t in enumerate(targets):
        if isinstance(t, str):
            t = T(t)
        elif isinstance(t, tuple):
            t = T(*t)
        t = dict(t)
        t["refId"] = chr(65 + i)
        out.append(t)
    return out


def _base(kind, title, desc, targets, ds=None):
    if not desc or not desc.strip():
        raise ValueError(f"panel {title!r} has no description")
    return {"type": kind, "title": title, "description": desc,
            "datasource": ds or PROM, "targets": _targets(targets)}


def stat(title, desc, targets, unit="short", th=NEUTRAL, color_mode="background",
         graph="none", decimals=None, mappings=None, no_value=None, text_mode="auto",
         calc="lastNotNull", min=None, max=None, orientation="auto"):
    p = _base("stat", title, desc, targets)
    d = {"unit": unit, "color": {"mode": "thresholds"}, "thresholds": th,
         "mappings": mappings or []}
    if decimals is not None:
        d["decimals"] = decimals
    if no_value is not None:
        d["noValue"] = no_value
    if min is not None:
        d["min"] = min
    if max is not None:
        d["max"] = max
    p["fieldConfig"] = {"defaults": d, "overrides": []}
    p["options"] = {"colorMode": color_mode, "graphMode": graph, "justifyMode": "center",
                    "textMode": text_mode, "orientation": orientation, "wideLayout": True,
                    "showPercentChange": False,
                    "reduceOptions": {"calcs": [calc], "fields": "", "values": False}}
    return p


def gauge(title, desc, targets, unit="percent", th=PCT, min=0, max=100, decimals=1):
    p = _base("gauge", title, desc, targets)
    p["fieldConfig"] = {"defaults": {"unit": unit, "min": min, "max": max, "decimals": decimals,
                                     "color": {"mode": "thresholds"}, "thresholds": th},
                        "overrides": []}
    p["options"] = {"showThresholdLabels": False, "showThresholdMarkers": True, "minVizHeight": 75,
                    "minVizWidth": 75, "sizing": "auto",
                    "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False}}
    return p


def bargauge(title, desc, targets, unit="percent", th=PCT, min=0, max=100, decimals=1,
             display="gradient", instant=True):
    if instant:
        targets = [dict(T(t) if isinstance(t, str) else T(*t) if isinstance(t, tuple) else t,
                        instant=True, range=False) for t in targets]
    p = _base("bargauge", title, desc, targets)
    d = {"unit": unit, "decimals": decimals, "color": {"mode": "thresholds"}, "thresholds": th}
    if min is not None:
        d["min"] = min
    if max is not None:
        d["max"] = max
    p["fieldConfig"] = {"defaults": d, "overrides": []}
    p["options"] = {"displayMode": display, "orientation": "horizontal", "showUnfilled": True,
                    "valueMode": "color", "namePlacement": "left", "sizing": "auto",
                    "minVizHeight": 14, "maxVizHeight": 24,
                    "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False}}
    return p


def ts(title, desc, targets, unit="short", stack=False, fill=18, decimals=None, min=None,
       max=None, calcs=("mean", "max", "lastNotNull"), overrides=None, th=None,
       draw="line", soft_max=None, percent_stack=False, legend="table", right=False):
    p = _base("timeseries", title, desc, targets)
    custom = {"drawStyle": draw, "lineInterpolation": "smooth", "lineWidth": 2,
              "fillOpacity": fill, "gradientMode": "opacity", "showPoints": "never",
              "pointSize": 4, "spanNulls": 60000, "axisBorderShow": False,
              "axisPlacement": "auto", "barAlignment": 0, "insertNulls": False,
              "stacking": {"mode": "percent" if percent_stack else ("normal" if stack else "none"),
                           "group": "A"},
              "thresholdsStyle": {"mode": "dashed" if th else "off"},
              "hideFrom": {"legend": False, "tooltip": False, "viz": False}}
    if draw == "bars":
        custom.update({"lineWidth": 1, "fillOpacity": 70, "gradientMode": "none"})
    if soft_max is not None:
        custom["axisSoftMax"] = soft_max
    d = {"unit": unit, "color": {"mode": "palette-classic"}, "custom": custom,
         "thresholds": th or steps((GREEN, None))}
    if decimals is not None:
        d["decimals"] = decimals
    if min is not None:
        d["min"] = min
    if max is not None:
        d["max"] = max
    p["fieldConfig"] = {"defaults": d, "overrides": overrides or []}
    p["options"] = {"legend": {"showLegend": legend != "hidden", "displayMode": "table" if legend == "table" else "list",
                               "placement": "right" if right else "bottom",
                               "calcs": list(calcs) if legend == "table" else [],
                               "sortBy": "Last *" if "lastNotNull" in calcs else "Max",
                               "sortDesc": True},
                    "tooltip": {"mode": "multi", "sort": "desc"}}
    return p


def ov_dashed(regex, color=None):
    """Override: series whose name matches `regex` drawn as a dashed limit line."""
    props = [{"id": "custom.lineStyle", "value": {"dash": [10, 10], "fill": "dash"}},
             {"id": "custom.fillOpacity", "value": 0}, {"id": "custom.lineWidth", "value": 2},
             {"id": "custom.stacking", "value": {"group": "A", "mode": "none"}}]
    if color:
        props.append({"id": "color", "value": {"fixedColor": color, "mode": "fixed"}})
    return {"matcher": {"id": "byRegexp", "options": regex}, "properties": props}


def ov_color(regex, color):
    return {"matcher": {"id": "byRegexp", "options": regex},
            "properties": [{"id": "color", "value": {"fixedColor": color, "mode": "fixed"}}]}


def ov_negative(regex):
    """Override: draw matching series below the axis (transmit vs receive)."""
    return {"matcher": {"id": "byRegexp", "options": regex},
            "properties": [{"id": "custom.transform", "value": "negative-Y"}]}


def timeline(title, desc, targets, mappings, th=None, row_height=0.8):
    p = _base("state-timeline", title, desc, targets)
    p["fieldConfig"] = {"defaults": {"color": {"mode": "thresholds"},
                                     "thresholds": th or steps((GREEN, None)),
                                     "mappings": mappings,
                                     "custom": {"fillOpacity": 80, "lineWidth": 0,
                                                "hideFrom": {"legend": False, "tooltip": False, "viz": False}}},
                        "overrides": []}
    p["options"] = {"mergeValues": True, "showValue": "auto", "alignValue": "center",
                    "rowHeight": row_height, "legend": {"showLegend": True, "displayMode": "list",
                                                        "placement": "bottom"},
                    "tooltip": {"mode": "single", "sort": "none"}}
    return p


def heatmap(title, desc, expr, unit="s", scheme="Spectral"):
    p = _base("heatmap", title, desc, [T(expr, "{{le}}", fmt="heatmap")])
    p["fieldConfig"] = {"defaults": {"custom": {"hideFrom": {"legend": False, "tooltip": False, "viz": False},
                                                "scaleDistribution": {"type": "linear"}}},
                        "overrides": []}
    p["options"] = {"calculate": False, "cellGap": 1, "cellValues": {"unit": "short"},
                    "color": {"mode": "scheme", "scheme": scheme, "exponent": 0.5, "fill": "dark-orange",
                              "reverse": False, "scale": "exponential", "steps": 64},
                    "exemplars": {"color": "rgba(255,0,255,0.7)"}, "filterValues": {"le": 1e-9},
                    "legend": {"show": True}, "rowsFrame": {"layout": "auto"},
                    "tooltip": {"mode": "single", "yHistogram": True, "showColorScale": False},
                    "yAxis": {"axisPlacement": "left", "reverse": False, "unit": unit}}
    return p


def table(title, desc, targets, columns, overrides=None, sort=None, links=None, hide=()):
    """Instant multi-query table merged on its label columns.

    columns: ordered list of (field, display, unit, extra) where field is a label
    name or "Value #<refId>" and extra is a dict of override properties, e.g.
    {"cell": "color-background", "th": PCT, "mappings": vmap(...)}.
    """
    tg = []
    for t in targets:
        t = T(t) if isinstance(t, str) else T(*t) if isinstance(t, tuple) else t
        tg.append(dict(t, instant=True, range=False, format="table"))
    p = _base("table", title, desc, tg)
    rename, index, ovs = {}, {}, []
    for i, (field, display, unit, extra) in enumerate(columns):
        rename[field] = display
        index[field] = i
        props = []
        if unit:
            props.append({"id": "unit", "value": unit})
        extra = extra or {}
        if "decimals" in extra:
            props.append({"id": "decimals", "value": extra["decimals"]})
        if "cell" in extra:
            mode = extra["cell"]
            cell = {"type": mode}
            if mode == "color-background":
                cell["mode"] = "gradient"
            if mode == "gauge":
                cell = {"type": "gauge", "mode": "lcd", "valueDisplayMode": "text"}
            props.append({"id": "custom.cellOptions", "value": cell})
        if "th" in extra:
            props.append({"id": "thresholds", "value": extra["th"]})
            props.append({"id": "color", "value": {"mode": "thresholds"}})
        if "mappings" in extra:
            props.append({"id": "mappings", "value": extra["mappings"]})
        if "min" in extra:
            props.append({"id": "min", "value": extra["min"]})
        if "max" in extra:
            props.append({"id": "max", "value": extra["max"]})
        if "width" in extra:
            props.append({"id": "custom.width", "value": extra["width"]})
        if "links" in extra:
            props.append({"id": "links", "value": extra["links"]})
        if "novalue" in extra:
            props.append({"id": "noValue", "value": extra["novalue"]})
        if props:
            ovs.append({"matcher": {"id": "byName", "options": display}, "properties": props})
    # Every label the queries keep must be either a listed column or hidden here,
    # otherwise it shows up as an extra column.
    exclude = {"Time": True, "__name__": True}
    for h in hide:
        exclude[h] = True
    p["transformations"] = [
        {"id": "merge", "options": {}},
        {"id": "organize", "options": {"excludeByName": exclude, "renameByName": rename,
                                       "indexByName": index, "includeByName": {}}},
    ]
    p["fieldConfig"] = {"defaults": {"custom": {"align": "auto", "cellOptions": {"type": "auto"},
                                                "inspect": False, "filterable": True},
                                     "color": {"mode": "thresholds"},
                                     "thresholds": steps((GREEN, None))},
                        "overrides": ovs + (overrides or [])}
    p["options"] = {"showHeader": True, "cellHeight": "sm", "footer": {"show": False, "reducer": ["sum"],
                                                                     "fields": "", "countRows": False}}
    if sort:
        p["options"]["sortBy"] = [{"displayName": sort[0], "desc": sort[1]}]
    return p


def logs(title, desc, expr):
    p = _base("logs", title, desc, [T(expr, ds=LOKI)], ds=LOKI)
    p["options"] = {"showTime": True, "showLabels": False, "showCommonLabels": False,
                    "wrapLogMessage": True, "prettifyLogMessage": False, "enableLogDetails": True,
                    "dedupStrategy": "none", "sortOrder": "Descending", "enableInfiniteScrolling": True}
    return p


def pod_link(dash_uid="6581e46e4e5c7ba40a07646395ef7b23", ns="namespace", pod="pod"):
    """Data link from a table cell to the Kubernetes / Compute Resources / Pod dashboard."""
    return [{"title": "Open pod resources",
             "url": f"/d/{dash_uid}?var-datasource=${{datasource}}&var-namespace=${{__data.fields.{ns}}}"
                    f"&var-pod=${{__data.fields.{pod}}}&${{__url_time_range}}",
             "targetBlank": False}]


# --------------------------------------------------------------------------------------------
# Template variables
# --------------------------------------------------------------------------------------------

def ds_var():
    return {"name": "datasource", "label": "Data source", "type": "datasource", "query": "prometheus",
            "current": {"selected": False, "text": "Prometheus", "value": "prometheus"},
            "hide": 0, "refresh": 1, "regex": "", "options": [], "includeAll": False, "multi": False}


def qvar(name, label, query, multi=True, include_all=True, all_value=None, regex="", hide=0, sort=1):
    v = {"name": name, "label": label, "type": "query", "datasource": PROM,
         # No qryType: Grafana then runs `query` verbatim (classic label_values),
         # instead of rebuilding it from metric/label fields this JSON does not set.
         "definition": query, "query": {"query": query, "refId": "PrometheusVariableQueryEditor-VariableQuery"},
         "refresh": 2, "regex": regex, "sort": sort, "hide": hide, "multi": multi,
         "includeAll": include_all, "options": [],
         "current": ({"selected": True, "text": ["All"], "value": ["$__all"]} if include_all and multi
                     else {"selected": True, "text": "All", "value": "$__all"} if include_all else {})}
    if all_value is not None:
        v["allValue"] = all_value
    return v


# --------------------------------------------------------------------------------------------
# Dashboard + grid layout
# --------------------------------------------------------------------------------------------

class Dash:
    def __init__(self, uid, title, description, tags, variables, links=None, time_from="now-3h",
                 refresh="30s"):
        self.uid, self.title, self.description = uid, title, description
        self.tags, self.variables, self.links = tags, variables, links or []
        self.time_from, self.refresh = time_from, refresh
        self.panels, self.y, self._id = [], 0, 0

    def _nid(self):
        self._id += 1
        return self._id

    def row(self, title):
        self.panels.append({"id": self._nid(), "type": "row", "title": title, "collapsed": False,
                            "gridPos": {"h": 1, "w": 24, "x": 0, "y": self.y}, "panels": []})
        self.y += 1

    def line(self, h, *items):
        """Place panels side by side on one grid line; widths must add up to 24."""
        widths = [w for _, w in items]
        if sum(widths) != 24:
            raise ValueError(f"{self.title}: line widths {widths} do not add up to 24 "
                             f"({[p['title'] for p, _ in items]})")
        x = 0
        for panel, w in items:
            panel = dict(panel)
            panel["id"] = self._nid()
            panel["gridPos"] = {"h": h, "w": w, "x": x, "y": self.y}
            self.panels.append(panel)
            x += w
        self.y += h

    def json(self):
        return {
            "annotations": {"list": [{"builtIn": 1, "datasource": {"type": "grafana", "uid": "-- Grafana --"},
                                      "enable": True, "hide": True, "iconColor": "rgba(0, 211, 255, 1)",
                                      "name": "Annotations & Alerts", "type": "dashboard"}]},
            "description": self.description,
            "editable": True,
            "fiscalYearStartMonth": 0,
            "graphTooltip": 1,
            "id": None,
            "links": self.links,
            "liveNow": False,
            "panels": self.panels,
            "refresh": self.refresh,
            "schemaVersion": 39,
            "tags": self.tags,
            "templating": {"list": self.variables},
            "time": {"from": self.time_from, "to": "now"},
            "timepicker": {},
            "timezone": "browser",
            "title": self.title,
            "uid": self.uid,
            "version": 1,
            "weekStart": "",
        }


def dash_links(*extra):
    """Dashboard-level links: the two dropdowns every built dashboard carries,
    plus any direct links passed in as (title, uid)."""
    out = [
        {"title": "Data services", "type": "dashboards", "tags": ["adhar-data"], "asDropdown": True,
         "includeVars": False, "keepTime": True, "targetBlank": False, "icon": "external link"},
        {"title": "Kubernetes", "type": "dashboards", "tags": ["adhar-kubernetes"], "asDropdown": True,
         "includeVars": False, "keepTime": True, "targetBlank": False, "icon": "external link"},
    ]
    for title, uid in extra:
        out.append({"title": title, "type": "link", "url": f"/d/{uid}", "asDropdown": False,
                    "includeVars": False, "keepTime": True, "targetBlank": False, "icon": "dashboard",
                    "tags": []})
    return out


def E(expr, **sel):
    """Substitute <<NAME>> placeholders (PromQL braces make f-strings unreadable)."""
    for k, v in sel.items():
        expr = expr.replace(f"<<{k}>>", v)
    if "<<" in expr:
        raise ValueError(f"unsubstituted placeholder in {expr}")
    return expr
