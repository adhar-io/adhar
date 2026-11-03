package apps

import (
	"strings"
	"testing"
)

// The CLI renders a skeleton exactly as the Console's renderer does
// (apps/console/app/server/template-render.ts), so the two commit the same
// repository from the same template. These cases are the Console's own
// (template-render_conditions_test.ts) plus the skeleton lines that diverged
// on 2026-11-03.

func render(t *testing.T, src string, values map[string]any) string {
	t.Helper()
	out, err := renderContent(src, map[string]any{"values": values})
	if err != nil {
		t.Fatalf("render %q: %v", src, err)
	}
	return out
}

// springboot-web-service's compose.yaml. `not (…)` was refused outright, which
// failed the whole scaffold with the template's defaults (all three false),
// and with any service on it was evaluated as TRUE, so compose.yaml rendered
// `services: {}` followed by the services — not YAML (transaction-service).
func TestNotOverAParenthesisedOrChoosesTheRightBranch(t *testing.T) {
	compose := strings.Join([]string{
		"{%- if not (values.withDatabase or values.withCache or values.withKafka) %}",
		"services: {}",
		"{%- else %}",
		"services:",
		"{%- endif %}",
		"{%- if values.withKafka %}",
		"  kafka: {}",
		"{%- endif %}",
	}, "\n")
	if got := strings.TrimSpace(render(t, compose, map[string]any{"withKafka": true})); got != "services:\n  kafka: {}" {
		t.Errorf("with kafka: %q", got)
	}
	if got := strings.TrimSpace(render(t, compose, map[string]any{"withDatabase": false, "withCache": false, "withKafka": false})); got != "services: {}" {
		t.Errorf("with nothing: %q", got)
	}
}

// Every web template's service.yaml. `values.chaosTesting == false` was looked
// up as a PATH, found nothing, and was never true: a service scaffolded with
// chaosTesting=false still carried its chaos experiments.
func TestAComparisonIsEvaluatedNotLookedUp(t *testing.T) {
	svc := "{%- if values.chaosTesting == false %}\n    adhar.io/chaos: \"false\"\n{%- else %}\n    adhar.io/chaos-experiments: \"pod-delete\"\n{%- endif %}\n"
	if got := render(t, svc, map[string]any{"chaosTesting": false}); !strings.Contains(got, `adhar.io/chaos: "false"`) {
		t.Errorf("chaosTesting=false rendered %q", got)
	}
	if got := render(t, svc, map[string]any{"chaosTesting": true}); !strings.Contains(got, "chaos-experiments") {
		t.Errorf("chaosTesting=true rendered %q", got)
	}
}

// react-spa-app's package.json: an inline `{% endif %}` followed by a line
// break. Dropping the break after every tag fused `"7.3.0"  },` (and in a
// README, a code fence onto the line before it, so it never closed).
func TestAnInlineTagKeepsTheLineBreakAfterIt(t *testing.T) {
	src := "    \"faro\": \"2.12.1\"{% if values.withLocalisation %},\n    \"@tolgee/react\": \"7.3.0\"{% endif %}\n  },\n"
	want := "    \"faro\": \"2.12.1\",\n    \"@tolgee/react\": \"7.3.0\"\n  },\n"
	if got := render(t, src, map[string]any{"withLocalisation": true}); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNestedIfBlocksRenderEachLevelAndLeaveNoTagsBehind(t *testing.T) {
	nested := strings.Join([]string{
		"deps:",
		"{%- if values.withDatabase or values.withCache %}",
		"{%- if values.withDatabase %}",
		"  - db",
		"{%- endif %}",
		"{%- if values.withCache %}",
		"  - cache",
		"{%- endif %}",
		"{%- endif %}",
		"end",
	}, "\n")
	for _, tc := range []struct {
		db, cache bool
		want      string
	}{
		{true, true, "deps:\n  - db\n  - cache\nend"},
		{false, true, "deps:\n  - cache\nend"},
		{false, false, "deps:\nend"},
	} {
		if got := render(t, nested, map[string]any{"withDatabase": tc.db, "withCache": tc.cache}); got != tc.want {
			t.Errorf("db=%v cache=%v: got %q, want %q", tc.db, tc.cache, got, tc.want)
		}
	}
}

func TestConditionPrecedenceAndComparisons(t *testing.T) {
	r := func(cond string, v map[string]any) string {
		return render(t, "{% if "+cond+" %}Y{% else %}N{% endif %}", v)
	}
	for _, tc := range []struct {
		cond string
		v    map[string]any
		want string
	}{
		{"a or b and c", map[string]any{"a": true, "b": false, "c": false}, "Y"},
		{"(a or b) and c", map[string]any{"a": true, "b": false, "c": false}, "N"},
		{"not a and b", map[string]any{"a": false, "b": true}, "Y"},
		{`values.kind == "jvm"`, map[string]any{"kind": "jvm"}, "Y"},
		{"values.withAuth == false", map[string]any{"withAuth": false}, "Y"},
		{"kind != 'jvm'", map[string]any{"kind": "jvm"}, "N"},
		{"values.replicas == 3", map[string]any{"replicas": 3}, "Y"},
		// The Console's truthiness: "0", "no" and "false" strings are false.
		{"values.flag", map[string]any{"flag": "no"}, "N"},
		{"values.flag", map[string]any{"flag": " False "}, "N"},
		{"values.flag", map[string]any{"flag": "0"}, "N"},
		{"values.flag", map[string]any{"flag": "yes"}, "Y"},
	} {
		if got := r(tc.cond, tc.v); got != tc.want {
			t.Errorf("{%% if %s %%} with %v = %s, want %s", tc.cond, tc.v, got, tc.want)
		}
	}
}

func TestElifPicksTheFirstTrueBranchOnly(t *testing.T) {
	tpl := "{% if a %}A{% elif b %}B{% elif c %}C{% else %}D{% endif %}"
	for _, tc := range []struct {
		v    map[string]any
		want string
	}{
		{map[string]any{"b": true, "c": true}, "B"},
		{map[string]any{"c": true}, "C"},
		{map[string]any{}, "D"},
	} {
		if got := render(t, tpl, tc.v); got != tc.want {
			t.Errorf("%v: got %q, want %q", tc.v, got, tc.want)
		}
	}
}

// A branch that is not taken is not evaluated — not even a condition this
// renderer would refuse — but its tags must still balance.
func TestAnUntakenBranchIsSkippedNestedTagsIncluded(t *testing.T) {
	tpl := "{% if a %}{% if b %}x{% else %}y{% endif %}{% else %}z{% endif %}"
	if got := render(t, tpl, map[string]any{}); got != "z" {
		t.Errorf("got %q, want z", got)
	}
	if got := render(t, tpl, map[string]any{"a": true}); got != "y" {
		t.Errorf("got %q, want y", got)
	}
	if got := render(t, "{% if a %}{% if b in c %}x{% endif %}{% endif %}ok", map[string]any{}); got != "ok" {
		t.Errorf("an untaken condition was evaluated: %q", got)
	}
}

func TestUnbalancedTagsAndUnsupportedConditionsFailLoudly(t *testing.T) {
	for src, want := range map[string]string{
		"{% if a %}x":                 "unclosed",
		"x{% endif %}":                "no matching",
		"{% else %}":                  "no matching",
		"{% if a in b %}x{% endif %}": "unexpected",
		"{% if (a %}x{% endif %}":     "parentheses",
		"{% if %}x{% endif %}":        "empty",
	} {
		_, err := renderContent(src, map[string]any{"values": map[string]any{"a": true}})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("render(%q) error = %v, want one mentioning %q", src, err, want)
		}
	}
}

func TestOtherTagsPassThroughUntouched(t *testing.T) {
	if got := render(t, "{% raw %}keep{% endraw %}", map[string]any{}); !strings.Contains(got, "{% raw %}") {
		t.Errorf("got %q", got)
	}
}

// A file's PATH is substituted, never branched on, as in the Console.
func TestASkeletonPathIsSubstitutedAndBinaryFilesAreKept(t *testing.T) {
	ctxVals := map[string]any{"values": map[string]any{"name": "billing"}}
	rel, content, err := renderSkeletonFile("cmd/${{ values.name }}/main.go", []byte("package ${{ values.name }}\n"), ctxVals)
	if err != nil || rel != "cmd/billing/main.go" || string(content) != "package billing\n" {
		t.Fatalf("got %q %q %v", rel, content, err)
	}
	// The Console commits an SVG verbatim; so does the CLI.
	svg := []byte("<svg><!-- ${{ values.name }} --></svg>")
	if _, content, _ := renderSkeletonFile("public/logo.svg", svg, ctxVals); string(content) != string(svg) {
		t.Fatalf("an SVG was rendered: %q", content)
	}
}

// `x or <default>` keeps a left side that is set, even the string "false" —
// the Console's rule — and an unparseable left side is an error, not a miss.
func TestOrKeepsAnySetValueAndRefusesAnUnparseableLeftSide(t *testing.T) {
	ctxVals := map[string]any{"values": map[string]any{"mode": "false", "port": 0}}
	if v, err := evalExpression("values.mode or 'on'", ctxVals); err != nil || v != "false" {
		t.Errorf("values.mode or 'on' = %v, %v", v, err)
	}
	if v, err := evalExpression("values.port or 8080", ctxVals); err != nil || stringify(v) != "8080" {
		t.Errorf("values.port or 8080 = %v, %v", v, err)
	}
	if _, err := evalExpression("not (values.mode or 'x'", ctxVals); err == nil {
		t.Error("an unparseable left side of `or` rendered")
	}
}

// The Console matches ${{ … }} across line breaks and writes a list the way
// JavaScript's String() does; the CLI left the first literal and wrote Go's
// `[en de]` for the second.
func TestExpressionsMatchTheConsolesSubstitution(t *testing.T) {
	values := map[string]any{"name": "billing", "languages": []any{"en", "de"}}
	if got := render(t, "app: ${{\n  values.name\n}}", values); got != "app: billing" {
		t.Errorf("a multi-line expression rendered %q", got)
	}
	if got := render(t, "languages: [${{ values.languages }}]", values); got != "languages: [en,de]" {
		t.Errorf("a list rendered %q", got)
	}
}
