package adharplatform

// No NaN on a dashboard, and an empty panel that says why it is empty.
//
// An idle service still publishes its counters, so a rate over a quiet window is
// 0, not absent: 0/0 is NaN, histogram_quantile over buckets that all rose by 0
// is NaN, scalar() of nothing is NaN, and a summary's or a Kafka client's
// windowed quantile / max / avg is NaN until it observes something. Grafana
// prints that literally. On the GCP cluster (2026-10-27) transaction-service read
// "NaN" in its 5xx Ratio, p95 Latency and Burn Rate stats, in every cell of its
// Routes and Dependencies tables and in the legends of its latency, persistence
// and Kafka panels — in dev and prod, where nobody had called it in the last few
// minutes — and 65 of the 1923 PromQL targets of the platform's own dashboards
// returned NaN (etcd and API latency, reconcile times, cache hit ratios, the 5xx
// ratio of an application nobody had called). The truth there is "nothing
// happened in this window", which a dropped point (the panel's no-value text, a
// gap in a line) says and neither NaN nor a made-up 0 does.
//
// The per-application dashboards guard by hand (nonzero() / known() in
// hack/dashboards/build-app-dashboard.py); the platform dashboards through one
// pass over every PromQL target as they are written (kube-prometheus/nanguard.py),
// which also covers the vendored kubernetes-mixin and the Adhar-authored JSON
// they embed. Both are checked here with the same rules.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// skipPromQLString returns the index of the quote closing the string literal
// that opens at s[i].
func skipPromQLString(s string, i int) int {
	q := s[i]
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			if q != '`' {
				j++
			}
		case q:
			return j
		}
	}
	return len(s) - 1
}

// closingBracket returns the index of the bracket closing the one at s[i].
func closingBracket(s string, i int, open, close byte) int {
	depth := 0
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '"', '\'', '`':
			j = skipPromQLString(s, j)
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return len(s) - 1
}

// closingParen returns the index of the parenthesis closing the one at s[i].
func closingParen(s string, i int) int { return closingBracket(s, i, '(', ')') }

func skipSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n') {
		i++
	}
	return i
}

var (
	promIdent  = regexp.MustCompile(`^[A-Za-z_:][A-Za-z0-9_:]*`)
	promNumber = regexp.MustCompile(`^[0-9]*\.?[0-9]+([eE][+-]?[0-9]+)?`)
	promVar    = regexp.MustCompile(`^\$\{[^}]*\}|^\$[A-Za-z_][A-Za-z0-9_]*`)
	promOffset = regexp.MustCompile(`^\s*-?[0-9A-Za-z_$]+`)
	promAt     = regexp.MustCompile(`^@\s*(start\(\)|end\(\)|[0-9.]+)`)
)

// promOperand returns the operand that starts at s[i]: a parenthesised
// expression, a number, a $variable, or a selector / function call /
// aggregation with its grouping, range, offset and @ modifiers. Vector-matching
// modifiers in front of it (on/ignoring, group_left/group_right) are skipped; a
// following `^`, which binds tighter than `/`, is included.
func promOperand(s string, i int) string {
	i = skipSpace(s, i)
	for {
		m := promIdent.FindString(s[i:])
		if m != "on" && m != "ignoring" && m != "group_left" && m != "group_right" {
			break
		}
		i = skipSpace(s, i+len(m))
		if i < len(s) && s[i] == '(' {
			i = skipSpace(s, closingParen(s, i)+1)
		}
	}
	start := i
	if i >= len(s) {
		return ""
	}
	end := i
	switch c := s[i]; {
	case c == '(':
		end = closingParen(s, i) + 1
	case c >= '0' && c <= '9' || c == '.':
		end = i + len(promNumber.FindString(s[i:]))
	case c == '$':
		end = i + max(len(promVar.FindString(s[i:])), 1)
	case c == '{':
		end = closingBracket(s, i, '{', '}') + 1
	default:
		name := promIdent.FindString(s[i:])
		if name == "" {
			return ""
		}
		end = i + len(name)
		grouping := func(k int) int {
			j := skipSpace(s, k)
			if g := promIdent.FindString(s[j:]); g == "by" || g == "without" {
				j = skipSpace(s, j+len(g))
				if j < len(s) && s[j] == '(' {
					return closingParen(s, j) + 1
				}
			}
			return k
		}
		end = grouping(end)
		if j := skipSpace(s, end); j < len(s) && s[j] == '(' {
			end = grouping(closingParen(s, j) + 1)
		}
		if end < len(s) && s[end] == '{' {
			end = closingBracket(s, end, '{', '}') + 1
		}
	}
	for {
		j := skipSpace(s, end)
		if j < len(s) && s[j] == '[' {
			end = strings.IndexByte(s[j:], ']') + j + 1
			continue
		}
		if promIdent.FindString(s[j:]) == "offset" {
			end = j + len("offset") + len(promOffset.FindString(s[j+len("offset"):]))
			continue
		}
		if m := promAt.FindString(s[j:]); m != "" {
			end = j + len(m)
			continue
		}
		break
	}
	if j := skipSpace(s, end); j < len(s) && s[j] == '^' {
		return s[start:j+1] + promOperand(s, j+1)
	}
	return s[start:end]
}

// promDivisors returns the right operand of every division in a PromQL
// expression (string literals, where a `/` is a path, are skipped).
func promDivisors(expr string) []string {
	var out []string
	for i := 0; i < len(expr); i++ {
		switch expr[i] {
		case '"', '\'', '`':
			i = skipPromQLString(expr, i)
		case '/':
			out = append(out, promOperand(expr, i+1))
		}
	}
	return out
}

// constantExpr: numbers, arithmetic and $variables only, like (1 - $slo).
var constantExpr = regexp.MustCompile(`^[0-9.eE\s+\-*/()]*(\$\{?[A-Za-z_][A-Za-z0-9_]*\}?[0-9.eE\s+\-*/()]*)*$`)

// endsWithPositiveFilter reports whether a parenthesised expression's content
// ends, at its own top level, with `> N` for N >= 0 (not `>= 0`, which keeps
// the zero, and not `> bool 0`, which turns it into 0 or 1).
func endsWithPositiveFilter(inner string) bool {
	depth, last := 0, -1
	for i := 0; i < len(inner); i++ {
		switch inner[i] {
		case '"', '\'', '`':
			i = skipPromQLString(inner, i)
		case '(', '{', '[':
			depth++
		case ')', '}', ']':
			depth--
		case '>':
			if depth == 0 {
				last = i
			}
		}
	}
	if last < 0 || last+1 < len(inner) && inner[last+1] == '=' {
		return false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(inner[last+1:]), 64)
	return err == nil && v >= 0
}

// guardedDivisor: a divisor that is never zero (and never an unguarded NaN).
func guardedDivisor(op string) bool {
	op = strings.TrimSpace(op)
	if v, err := strconv.ParseFloat(op, 64); err == nil {
		return v != 0
	}
	if strings.HasPrefix(op, "$") {
		return true // a dashboard constant ($slo, ...)
	}
	if strings.HasPrefix(op, "clamp_min(") && closingParen(op, len("clamp_min")) == len(op)-1 {
		args := op[len("clamp_min(") : len(op)-1]
		cut := strings.LastIndexByte(args, ',')
		v, err := strconv.ParseFloat(strings.TrimSpace(args[cut+1:]), 64)
		return cut > 0 && err == nil && v > 0
	}
	if strings.HasPrefix(op, "(") && closingParen(op, 0) == len(op)-1 {
		inner := op[1 : len(op)-1]
		return constantExpr.MatchString(inner) || endsWithPositiveFilter(inner)
	}
	return false
}

// filteredAfter: the text after a value-producing call or selector filters its
// NaN away — `>= 0` or `> 0`, optionally after a unit division like `/ 1000`.
var filteredAfter = regexp.MustCompile(`^\s*(/\s*[0-9.]+([eE][+-]?[0-9]+)?\s*)?>=?\s*0([^0-9.]|$)`)

// unfilteredQuantiles returns every histogram_quantile call whose result is not
// filtered with `>= 0` (NaN compares false, so the filter is what drops a
// quantile of an idle window).
func unfilteredQuantiles(expr string) []string {
	var out []string
	for i := strings.Index(expr, "histogram_quantile("); i >= 0; {
		open := i + len("histogram_quantile")
		end := closingParen(expr, open)
		if !filteredAfter.MatchString(expr[end+1:]) {
			out = append(out, expr[i:end+1])
		}
		next := strings.Index(expr[end+1:], "histogram_quantile(")
		if next < 0 {
			break
		}
		i = end + 1 + next
	}
	return out
}

// Instant selectors whose series publish NaN themselves: a summary's (or a
// recorded) quantile with no observations in its window, and a Kafka client's
// windowed stats (records-lag-max, request-latency-avg, ...), NaN whenever the
// client recorded nothing in the last 30 s — every idle consumer (Micrometer
// passes the NaN through).
var nanSelector = regexp.MustCompile(`(?:\b(kafka_[a-z0-9_]+_(?:max|avg))|[A-Za-z_:][A-Za-z0-9_:]*)(\{[^{}]*\})`)

func unfilteredNaNSelectors(expr string) []string {
	var out []string
	for _, m := range nanSelector.FindAllStringSubmatchIndex(expr, -1) {
		matchers := expr[m[4]:m[5]]
		kafka := m[2] >= 0
		quantile := regexp.MustCompile(`(^|[{,\s])quantile\s*=~?\s*"`).MatchString(matchers)
		if !kafka && !quantile {
			continue
		}
		rest := expr[m[1]:]
		if strings.HasPrefix(strings.TrimSpace(rest), "[") || filteredAfter.MatchString(rest) {
			continue // a range vector (inside *_over_time), or already filtered
		}
		out = append(out, expr[m[0]:m[1]])
	}
	return out
}

// nanCheck runs every rule on one PromQL expression and counts what it looked at.
type nanCheck struct{ divisions, quantiles int }

func (c *nanCheck) problems(expr string) []string {
	var out []string
	for _, d := range promDivisors(expr) {
		c.divisions++
		if !guardedDivisor(d) {
			out = append(out, fmt.Sprintf("divides by %q, which is 0 (or absent-as-NaN) over an idle window", d))
		}
	}
	c.quantiles += strings.Count(expr, "histogram_quantile(")
	for _, q := range unfilteredQuantiles(expr) {
		out = append(out, fmt.Sprintf("%s is NaN over a window without observations; filter it with >= 0", q))
	}
	for _, s := range unfilteredNaNSelectors(expr) {
		out = append(out, fmt.Sprintf("%s publishes NaN itself; filter it with >= 0", s))
	}
	if e := strings.TrimSpace(expr); strings.HasPrefix(e, "scalar(") && closingParen(e, len("scalar")) == len(e)-1 {
		out = append(out, "a whole target in scalar() is NaN when the series is absent; plot the vector")
	}
	return out
}

type nanTarget struct {
	Datasource json.RawMessage `json:"datasource"`
	Expr       string          `json:"expr"`
}

type nanPanel struct {
	Type        string          `json:"type"`
	Title       string          `json:"title"`
	Datasource  json.RawMessage `json:"datasource"`
	Targets     []nanTarget     `json:"targets"`
	Panels      []nanPanel      `json:"panels"`
	FieldConfig struct {
		Defaults struct {
			NoValue  string `json:"noValue"`
			Mappings []struct {
				Options map[string]struct {
					Text  string `json:"text"`
					Color string `json:"color"`
				} `json:"options"`
			} `json:"mappings"`
		} `json:"defaults"`
	} `json:"fieldConfig"`
}

func nanPanels(t *testing.T, body string) []nanPanel {
	t.Helper()
	var d struct {
		Panels []nanPanel `json:"panels"`
	}
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatalf("dashboard JSON: %v", err)
	}
	var all []nanPanel
	var walk func([]nanPanel)
	walk = func(ps []nanPanel) {
		for _, p := range ps {
			all = append(all, p)
			walk(p.Panels)
		}
	}
	walk(d.Panels)
	return all
}

// promQLUIDs are the datasources whose targets are PromQL: the provisioned
// Prometheus and Mimir, and the folder dashboards' ${datasource} variable.
var promQLUIDs = map[string]bool{"prometheus": true, "mimir": true, "${datasource}": true, "$datasource": true}

// promQLTargets returns the PromQL expressions of a panel (a target without a
// datasource inherits the panel's; a panel without one uses the default,
// Prometheus).
func promQLTargets(p nanPanel) []string {
	isProm := func(raw json.RawMessage, inherit bool) bool {
		var o struct {
			Type string `json:"type"`
			UID  string `json:"uid"`
		}
		if len(raw) == 0 || string(raw) == "null" {
			return inherit
		}
		if json.Unmarshal(raw, &o.UID) != nil {
			if json.Unmarshal(raw, &o) != nil {
				return false
			}
		}
		if o.UID == "-- Mixed --" || o.UID == "" && o.Type == "" {
			return inherit
		}
		return o.Type == "prometheus" || promQLUIDs[o.UID]
	}
	panelProm := isProm(p.Datasource, true)
	var out []string
	for _, tg := range p.Targets {
		if tg.Expr != "" && isProm(tg.Datasource, panelProm) {
			out = append(out, tg.Expr)
		}
	}
	return out
}

func TestAppDashboardsNeverDivideByAnIdleRate(t *testing.T) {
	// The checks themselves, on expressions the generators used to emit.
	for expr, want := range map[string]bool{
		`(sum (rate(x_count{status=~"5.."}[5m])) or sum (rate(x_count[5m])) * 0) / sum (rate(x_count[5m]))`:       false,
		`(sum (rate(x_count{status=~"5.."}[5m])) or sum (rate(x_count[5m])) * 0) / (sum (rate(x_count[5m])) > 0)`: true,
		`sum by (le) (rate(b[5m])) / scalar(sum(rate(c[5m])))`:                                                    false,
		`sum by (le) (rate(b[5m])) / on() group_left() (sum(rate(c[5m])) > 0)`:                                    true,
		`sum(rate(c{uri!~"/actuator.*|/q/(health|metrics).*"}[5m])) / clamp_min(sum(rate(p[5m])), 1)`:             true,
		`sum(rate(s[5m])) / (sum(rate(c[5m])) >= 0)`:                                                              false,
		`1 - (sum(rate(a[1h])) / (sum(rate(b[1h])) > 0)) / (1 - $slo)`:                                            true,
		`sum(rate(alloc[5m])) / 1e9`:                                         true,
		`sum(w) / sum by (pod) (limits{resource="memory"})`:                  false,
		`rate(x[5m] offset 1h) / rate(y[5m] offset 1h)`:                      false,
		`rate(x[5m] offset 1h) / (rate(y[5m] offset 1h) > 0)`:                true,
		`(rate(r[2m]) / (rate(c[2m]) > 0)) * on (instance) group_left (n) u`: true,
	} {
		var c nanCheck
		if got := len(c.problems(expr)) == 0; got != want {
			t.Errorf("guarded(%s) = %v, want %v (divisors %q)", expr, got, want, promDivisors(expr))
		}
	}
	var c nanCheck
	for expr, want := range map[string]int{
		`histogram_quantile(0.95, sum by (le) (rate(b[5m])))`:                                                        1,
		`(histogram_quantile(0.95, sum by (le) (rate(b[5m]))) / 1000 >= 0) or (histogram_quantile(0.5, x) >= 0)`:     0,
		`max by (topic) (kafka_consumer_fetch_manager_records_lag_max{namespace="$env"})`:                            1,
		`max by (topic) (kafka_consumer_fetch_manager_records_lag_max{namespace="$env"} >= 0)`:                       0,
		`max by (topic) (kafka_consumer_fetch_manager_records_lag{namespace="$env"})`:                                0,
		`max by (slice) (prometheus_engine_query_duration_seconds{job="p", quantile="0.99"})`:                        1,
		`max_over_time(prometheus_engine_query_duration_seconds{quantile="0.99"}[5m]) > 0`:                           0,
		`scalar(max(kube_resourcequota{namespace="$namespace", type="hard", resource="requests.cpu"}))`:              1,
		`sum(rate(container_cpu_usage_seconds_total[5m])) - scalar(max(kube_resourcequota{namespace="$namespace"}))`: 0,
	} {
		if got := len(c.problems(expr)); got != want {
			t.Errorf("%s: %d problems, want %d: %q", expr, got, want, c.problems(expr))
		}
	}

	for _, kind := range dashboardTypes {
		t.Run(kind, func(t *testing.T) {
			body, _ := dashboardOfType(t, kind)
			var c nanCheck
			for _, p := range nanPanels(t, body) {
				for _, expr := range promQLTargets(p) {
					for _, problem := range c.problems(expr) {
						t.Errorf("panel %q %s: Grafana shows NaN (nonzero() / known() in build-app-dashboard.py)", p.Title, problem)
					}
				}
			}
			// Each template has ratios and quantiles to guard; finding none means this
			// check stopped looking, not that the template is safe.
			if c.divisions < 4 || (kind != "frontend" && c.quantiles < 4) {
				t.Errorf("checked %d divisions and %d quantiles; the scanner is not reading the template", c.divisions, c.quantiles)
			}
		})
	}
}

// The platform's own dashboards: every folder dashboard (build-service-dashboards.py)
// and the front page (build-platform-dashboard.py), guarded on output by nanguard.py.
func TestPlatformDashboardsNeverDivideByAnIdleRate(t *testing.T) {
	bodies := map[string]string{"adhar-platform": paPlatformDashboardJSON(t)}
	for _, name := range builtDashboardNames(t) {
		_, body := loadBuiltDashboard(t, name)
		bodies[name] = body
	}
	var c nanCheck
	for name, body := range bodies {
		for _, p := range nanPanels(t, body) {
			for _, expr := range promQLTargets(p) {
				for _, problem := range c.problems(expr) {
					t.Errorf("%s: panel %q %s: Grafana shows NaN. The builders run every target through "+
						"kube-prometheus/nanguard.py; re-run them, or teach nanguard the new shape", name, p.Title, problem)
				}
			}
		}
	}
	// ~1900 PromQL targets with hundreds of divisions and quantiles between them.
	if c.divisions < 150 || c.quantiles < 50 {
		t.Errorf("checked %d divisions and %d quantiles across %d dashboards; the scanner is not reading them",
			c.divisions, c.quantiles, len(bodies))
	}
}

// A guarded panel is empty when its window was idle, so its no-value text must
// say that first: "the app publishes no X" is false for an app that has X and
// served nothing (transaction-service in test had only probe series since its
// last restart, and every RED panel claimed it had no HTTP metrics).
func TestGuardedPanelsExplainAnIdleWindow(t *testing.T) {
	idle := regexp.MustCompile(`(?i)in this window|^No requests`)
	traffic := regexp.MustCompile(`\b(rate|irate|increase)\(`)
	// idleSensitive: a target that yields nothing over an idle window — a
	// quantile, or a ratio whose divisor is a rate FILTERED with `> 0` (a
	// clamp_min divisor turns the idle window into 0 instead, and draws it).
	idleSensitive := func(expr string) bool {
		if strings.Contains(expr, "histogram_quantile(") {
			return true
		}
		for _, d := range promDivisors(expr) {
			d = strings.TrimSpace(d)
			if strings.HasPrefix(d, "(") && endsWithPositiveFilter(d[1:len(d)-1]) && traffic.MatchString(d) {
				return true
			}
		}
		return false
	}
	for _, kind := range dashboardTypes {
		t.Run(kind, func(t *testing.T) {
			body, _ := dashboardOfType(t, kind)
			checked := 0
			for _, p := range nanPanels(t, body) {
				exprs := promQLTargets(p)
				if len(exprs) == 0 || len(exprs) != len(p.Targets) {
					continue // a panel with a Loki or Tempo target has more than one reason to be empty
				}
				all := true
				for _, e := range exprs {
					all = all && idleSensitive(e)
				}
				if !all {
					continue // an unguarded series (a rate, a gauge) keeps the panel drawn when idle
				}
				checked++
				if nv := p.FieldConfig.Defaults.NoValue; !idle.MatchString(nv) {
					t.Errorf("panel %q is empty whenever its window is idle, but its no-value text %q does not say so",
						p.Title, nv)
				}
			}
			if kind != "frontend" && checked < 5 {
				t.Errorf("checked %d idle-sensitive panels; the scanner is not reading the template", checked)
			}
		})
	}
}

// The status stat that says WHY the browser section is empty. Its codes come
// from the query (`* 0 + N` per branch, vector(N) last) and each one needs a
// value mapping, or Grafana shows a bare number.
func TestTheFrontendSaysWhyItHasNoBrowserTelemetry(t *testing.T) {
	branch := regexp.MustCompile(`\(sum\(count_over_time\((.+?) \[(\$__range|7d)\]\)\) > 0\) \* 0 \+ (\d+)`)
	for _, kind := range []string{"frontend", "fullstack"} {
		t.Run(kind, func(t *testing.T) {
			body, d := dashboardOfType(t, kind)
			_, section := frontendSection(t, d)
			if len(section) == 0 || section[0].Title != "Browser Telemetry" || section[0].Type != "stat" {
				t.Fatalf("the Frontend row does not open on the Browser Telemetry status stat")
			}
			var status nanPanel
			for _, p := range nanPanels(t, body) {
				if p.Title == "Browser Telemetry" {
					status = p
				}
			}
			if len(status.Targets) != 1 || paDSUID(status.Targets[0].Datasource) != "loki" {
				t.Fatal("Browser Telemetry must be one Loki query")
			}
			expr := status.Targets[0].Expr
			if !regexp.MustCompile(`or vector\(\d+\)$`).MatchString(expr) {
				t.Error("Browser Telemetry must end in `or vector(N)`, so it always answers")
			}
			faro := `{source="faro",app_name="cart",app_environment="${env:text}"}`
			conds := map[string]string{}
			for _, m := range branch.FindAllStringSubmatch(expr, -1) {
				conds[m[3]] = m[1] + " [" + m[2] + "]"
			}
			wants := map[string][]string{
				"1": {faro + " [$__range]"},                         // this environment, this range
				"4": {`{source="faro",app_name="cart"} [$__range]`}, // any environment
				"5": {faro + " [7d]"},                               // this environment, recently
			}
			if kind == "frontend" {
				// What the pod's nginx served: the page's own config fetch (only the
				// page's JavaScript asks for it, and never from cache) ...
				wants["2"] = []string{`adhar_environment="${env:text}", adhar_component="app"}`, `|= "GET /runtime-config.json"`, `|= "Mozilla/"`}
				// ... and a browser that rendered the page: its sub-resources carry the
				// page as Referer. A browser user agent alone is a link preview, a
				// crawler or a script (one fetched only `/` on 2026-10-27 and turned
				// every environment "Not instrumented").
				wants["3"] = []string{`adhar_component="app"}`, `|= "Mozilla/"`, `\"<referer>\"`, `| referer=~"https?://.+"`}
				if strings.HasSuffix(strings.TrimSuffix(conds["3"], " [$__range]"), `|= "Mozilla/"`) {
					t.Error("Not instrumented must not fire on a browser user agent alone")
				}
			} else if strings.Contains(expr, "runtime-config.json") || strings.Contains(expr, "referer") {
				t.Error("a server-rendered app has no nginx access log to read visits from")
			}
			for code, parts := range wants {
				for _, w := range parts {
					if !strings.Contains(conds[code], w) {
						t.Errorf("Browser Telemetry answers %s on %q, which lacks %s", code, conds[code], w)
					}
				}
			}
			if len(conds) != len(wants) {
				t.Errorf("Browser Telemetry has branches %v, want exactly %d", conds, len(wants))
			}
			codes := map[string]bool{}
			for _, m := range regexp.MustCompile(`\* 0 \+ (\d+)|vector\((\d+)\)`).FindAllStringSubmatch(expr, -1) {
				codes[m[1]+m[2]] = true
			}
			mapped := map[string]string{}
			for _, m := range status.FieldConfig.Defaults.Mappings {
				for k, o := range m.Options {
					mapped[k] = o.Text
				}
			}
			for c := range codes {
				if mapped[c] == "" {
					t.Errorf("Browser Telemetry can answer %s, which has no value mapping", c)
				}
			}
			if mapped["1"] != "Receiving" || len(codes) < 4 {
				t.Errorf("Browser Telemetry answers %v with mappings %v", codes, mapped)
			}

			// Every other browser panel's empty state points at the status instead of guessing.
			for _, p := range nanPanels(t, body) {
				nv := p.FieldConfig.Defaults.NoValue
				if strings.Contains(nv, "check FARO_URL") {
					t.Errorf("panel %q still guesses why it is empty: %q", p.Title, nv)
				}
				if strings.Contains(nv, "Faro events") && !strings.Contains(nv, "Browser Telemetry") {
					t.Errorf("panel %q says there are no Faro events without pointing at Browser Telemetry", p.Title)
				}
			}
		})
	}
}
