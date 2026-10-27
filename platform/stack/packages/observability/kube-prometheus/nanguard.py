"""No NaN on a dashboard: every builder in this directory passes its dashboard
through guard_dashboard() before writing it (dashlib.Dash.json for the folder
dashboards, build-platform-dashboard.py for the front page), so the rule holds
for hand-written panels, the vendored Adhar JSON and the kubernetes-mixin alike.

An idle service still publishes its counters and histograms, so over a quiet
window a rate is 0, not absent. 0/0 is NaN, histogram_quantile over buckets
that all rose by 0 is NaN, scalar() of nothing is NaN, and a summary's (or a
Kafka client's) windowed quantile / max / avg is NaN until it observes
something. Grafana prints that literally: "NaN" in stats, table cells and
legends. On the GCP cluster (2026-10-27) 65 of the 1923 PromQL targets of the
folder dashboards and the front page returned NaN — etcd and API latency,
reconcile times, cache hit ratios, the 5xx ratio of an application nobody had
called. The truth in every case is "nothing happened in this window", which a
dropped point says (a gap in the line, the panel's no-value text) and neither
NaN nor a made-up 0 does.

guard_expr rewrites one PromQL expression:

    a / b                         a / (b > 0)        a divisor that can be 0
    histogram_quantile(q, h)      (histogram_quantile(q, h) >= 0)
    scalar(v)   (a whole target)  v                  absent instead of NaN
    m{quantile="0.99"}            (m{quantile="0.99"} >= 0)   summaries, recorded quantiles
    kafka_..._max|avg{...}        (kafka_..._max{...} >= 0)   Kafka client windowed stats

A divisor that cannot be 0 is left alone: a non-zero number, clamp_min(x, c)
with c > 0, a parenthesised constant or $variable, and one already filtered
with `> N`. NaN compares false, so `>= 0` drops a NaN quantile and keeps every
real one (latencies, sizes and lags are never negative). The Go guard
TestPlatformDashboardsNeverDivideByAnIdleRate (dashboard_no_nan_test.go) checks
the written manifests with the same rules.
"""
import re

_IDENT = re.compile(r"[A-Za-z_:][A-Za-z0-9_:]*")
_NUMBER = re.compile(r"[0-9]*\.?[0-9]+(?:[eE][+-]?[0-9]+)?")
_MODIFIERS = ("on", "ignoring", "group_left", "group_right")
# Already filtered: `>= 0`, `> 0` (optionally after a unit division like `/ 1e6`).
_FILTERED = re.compile(r"\s*(?:/\s*[0-9.]+(?:[eE][+-]?[0-9]+)?\s*)?>=?\s*0(?![0-9.])")
_KAFKA_WINDOWED = re.compile(r"^kafka_[a-z0-9_]+_(?:max|avg)$")
_QUANTILE_MATCHER = re.compile(r"(?:^|[{,\s])quantile\s*=~?\s*\"")
PROM_UIDS = {"prometheus", "mimir", "${datasource}", "$datasource", "${DS_PROMETHEUS}"}


def _skip_string(s, i):
    """Index of the quote closing the string literal that opens at s[i]."""
    q = s[i]
    j = i + 1
    while j < len(s):
        if s[j] == "\\" and q != "`":
            j += 2
            continue
        if s[j] == q:
            return j
        j += 1
    return len(s) - 1


def _closing(s, i, open_="(", close=")"):
    """Index of the bracket closing the one at s[i]."""
    depth = 0
    j = i
    while j < len(s):
        c = s[j]
        if c in "\"'`":
            j = _skip_string(s, j)
        elif c == open_:
            depth += 1
        elif c == close:
            depth -= 1
            if depth == 0:
                return j
        j += 1
    return len(s) - 1


def _space(s, i):
    while i < len(s) and s[i] in " \t\n":
        i += 1
    return i


def _operand(s, i):
    """(start, end) of the operand that starts at s[i] (a division's right side):
    a parenthesised expression, a number, a $variable, or a selector / function
    call / aggregation with its grouping, range, offset and @ modifiers. Vector
    matching modifiers in front of it (on/ignoring, group_left/group_right) are
    skipped. A following `^` binds tighter than `/` and is included."""
    i = _space(s, i)
    while True:
        m = _IDENT.match(s, i)
        if not m or m.group(0) not in _MODIFIERS:
            break
        i = _space(s, m.end())
        if i < len(s) and s[i] == "(":
            i = _space(s, _closing(s, i) + 1)
    start = i
    if i >= len(s):
        return start, start
    c = s[i]
    if c == "(":
        end = _closing(s, i) + 1
    elif c.isdigit() or c == ".":
        end = _NUMBER.match(s, i).end()
    elif c == "$":
        m = re.compile(r"\$\{[^}]*\}|\$[A-Za-z_][A-Za-z0-9_]*").match(s, i)
        end = m.end() if m else i + 1
    elif c == "{":
        end = _closing(s, i, "{", "}") + 1
    else:
        m = _IDENT.match(s, i)
        if not m:
            return start, start
        end = m.end()

        def grouping(k):
            j = _space(s, k)
            g = _IDENT.match(s, j)
            if g and g.group(0) in ("by", "without"):
                j = _space(s, g.end())
                if j < len(s) and s[j] == "(":
                    return _closing(s, j) + 1
            return k

        end = grouping(end)
        j = _space(s, end)
        if j < len(s) and s[j] == "(":
            end = grouping(_closing(s, j) + 1)
        if end < len(s) and s[end] == "{":
            end = _closing(s, end, "{", "}") + 1
    # range, offset, @
    while True:
        j = _space(s, end)
        if j < len(s) and s[j] == "[":
            end = s.index("]", j) + 1
            continue
        m = _IDENT.match(s, j)
        if m and m.group(0) == "offset":
            d = re.compile(r"\s*-?[0-9A-Za-z_$]+").match(s, m.end())
            end = d.end() if d else m.end()
            continue
        if j < len(s) and s[j] == "@":
            d = re.compile(r"@\s*(?:start\(\)|end\(\)|[0-9.]+)").match(s, j)
            end = d.end() if d else j + 1
            continue
        break
    j = _space(s, end)
    if j < len(s) and s[j] == "^":
        _, end = _operand(s, j + 1)
    return start, end


_CONSTANT = re.compile(r"^[0-9.eE\s+\-*/()]*(?:\$\{?[A-Za-z_][A-Za-z0-9_]*\}?[0-9.eE\s+\-*/()]*)*$")


def _top_level_filter(inner):
    """True when `inner` ends, at its own top level, with `> N` for N >= 0 (not
    `>=`, which keeps the zero, and not `> bool N`, which turns it into 0 or 1)."""
    depth, last = 0, -1
    i = 0
    while i < len(inner):
        c = inner[i]
        if c in "\"'`":
            i = _skip_string(inner, i)
        elif c in "({[":
            depth += 1
        elif c in ")}]":
            depth -= 1
        elif c == ">" and depth == 0:
            last = i
        i += 1
    if last < 0 or inner[last + 1:last + 2] == "=":
        return False
    try:
        return float(inner[last + 1:].strip()) >= 0
    except ValueError:
        return False


def _safe_divisor(op):
    op = op.strip()
    if not op:
        return True
    try:
        return float(op) != 0
    except ValueError:
        pass
    if op.startswith("$"):
        return True                                   # a dashboard constant ($slo, ...)
    if op.startswith("clamp_min(") and _closing(op, len("clamp_min")) == len(op) - 1:
        args = op[len("clamp_min("):-1]
        try:
            return float(args[args.rindex(",") + 1:].strip()) > 0
        except ValueError:
            return False
    if op.startswith("(") and _closing(op, 0) == len(op) - 1:
        inner = op[1:-1]
        return bool(_CONSTANT.match(inner)) or _top_level_filter(inner)
    if op.startswith("scalar("):
        return True                                   # a scalar cannot be filtered; TestPlatformDashboards... flags it
    return False


def _strings_and_code(s):
    """Indices of s that are code (outside string literals)."""
    code = [True] * len(s)
    i = 0
    while i < len(s):
        if s[i] in "\"'`":
            j = _skip_string(s, i)
            for k in range(i, j + 1):
                code[k] = False
            i = j + 1
        else:
            i += 1
    return code


def _guard_divisions(expr):
    for _ in range(500):
        code = _strings_and_code(expr)
        for i, c in enumerate(expr):
            if c != "/" or not code[i]:
                continue
            s, e = _operand(expr, i + 1)
            op = expr[s:e]
            if not _safe_divisor(op):
                expr = f"{expr[:s]}({op} > 0){expr[e:]}"
                break
        else:
            return expr
    raise RuntimeError(f"nanguard: divisions did not converge: {expr}")


def _guard_quantiles(expr):
    i = 0
    pat = re.compile(r"(?<![A-Za-z0-9_:])histogram_quantile\s*\(")
    while True:
        code = _strings_and_code(expr)
        m = pat.search(expr, i)
        while m and not code[m.start()]:
            m = pat.search(expr, m.end())
        if not m:
            return expr
        end = _closing(expr, m.end() - 1)
        if _FILTERED.match(expr, end + 1):
            i = end + 1
            continue
        expr = f"{expr[:m.start()]}({expr[m.start():end + 1]} >= 0){expr[end + 1:]}"
        i = end + 2


def _guard_selectors(expr):
    """Summary / recorded quantiles and Kafka client windowed stats publish NaN
    themselves: filter each instant selector of them with `>= 0`."""
    out, i = [], 0
    code = _strings_and_code(expr)
    while i < len(expr):
        m = _IDENT.match(expr, i) if code[i] and (i == 0 or not re.match(r"[A-Za-z0-9_:$.]", expr[i - 1])) else None
        if not m:
            out.append(expr[i])
            i += 1
            continue
        name, j = m.group(0), m.end()
        if j < len(expr) and expr[j] == "{":
            k = _closing(expr, j, "{", "}") + 1
            matchers = expr[j:k]
            nxt = _space(expr, k)
            ranged = nxt < len(expr) and expr[nxt] == "["
            filtered = bool(_FILTERED.match(expr, k))
            prev = expr[:i].rstrip()
            wrapped = prev.endswith("(") and filtered
            if not ranged and not filtered and not wrapped and (
                    _QUANTILE_MATCHER.search(matchers) or _KAFKA_WINDOWED.match(name)):
                out.append(f"({name}{matchers} >= 0)")
            else:
                out.append(name + matchers)
            i = k
            continue
        out.append(name)
        i = j
    return "".join(out)


def _unwrap_scalar(expr):
    e = expr.strip()
    if e.startswith("scalar(") and _closing(e, len("scalar")) == len(e) - 1:
        return e[len("scalar("):-1]
    return expr


def guard_expr(expr):
    """The expression with every NaN source guarded (idempotent)."""
    expr = _unwrap_scalar(expr)
    expr = _guard_selectors(expr)
    expr = _guard_quantiles(expr)
    return _guard_divisions(expr)


def _is_prom(ds, default=True):
    if ds is None or ds == "":
        return default
    if isinstance(ds, str):
        return ds in PROM_UIDS
    if ds.get("type") == "prometheus":
        return True
    uid = ds.get("uid")
    if uid in ("-- Mixed --", None, ""):
        return default
    return uid in PROM_UIDS


def guard_panels(panels):
    for p in panels:
        panel_prom = _is_prom(p.get("datasource"))
        for t in p.get("targets") or []:
            if t.get("expr") and _is_prom(t.get("datasource"), panel_prom):
                t["expr"] = guard_expr(t["expr"])
        guard_panels(p.get("panels") or [])
    return panels


def guard_dashboard(dash):
    guard_panels(dash.get("panels") or [])
    return dash
