package apps

import (
	"encoding/json"
	"strings"
	"testing"
)

// `| dump` is JSON encoding, and the skeletons rely on it for values dropped
// straight into source code: `const APP_NAME = ${{ values.name | dump }}`
// needs the QUOTES.
//
// This CLI refused every piped expression outright, so it could not render the
// shipped react-spa-app template at all. The Console's renderer was worse — it
// fell through to a path lookup of the whole expression, found nothing, and
// emitted an empty string, so every scaffolded app was created with
//
//	const APP_NAME =
//	const DESCRIPTION =
//	const OWNER =
//
// and failed its first build at `vite build` with `Unexpected "const"`.
func TestDumpFilterJSONEncodesForSourceCode(t *testing.T) {
	ctx := map[string]any{"values": map[string]any{
		"name":        "reactapp",
		"description": `An app with "quotes" and` + "\na newline",
	}}

	got, err := evalExpression("values.name | dump", ctx)
	if err != nil {
		t.Fatalf("dump returned an error: %v", err)
	}
	if got != `"reactapp"` {
		t.Errorf("dump rendered %q, want a quoted JSON string", got)
	}

	// A description with quotes and newlines must still produce a valid literal.
	d, err := evalExpression("values.description | dump", ctx)
	if err != nil {
		t.Fatal(err)
	}
	var back string
	if err := json.Unmarshal([]byte(stringify(d)), &back); err != nil {
		t.Errorf("dump produced something that is not a JSON literal: %q (%v)", d, err)
	}
	if back != ctx["values"].(map[string]any)["description"] {
		t.Errorf("dump round-trip changed the value: %q", back)
	}

	// A missing value must be `null`, not empty — `const X = ` is a syntax error.
	miss, err := evalExpression("values.nope | dump", ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stringify(miss) != "null" {
		t.Errorf("a missing value dumped to %q, want null", stringify(miss))
	}
}

func TestLowerAndUpperFilters(t *testing.T) {
	ctx := map[string]any{"values": map[string]any{"n": "Ab"}}
	for expr, want := range map[string]string{
		"values.n | lower": "ab",
		"values.n | upper": "AB",
	} {
		v, err := evalExpression(expr, ctx)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		if stringify(v) != want {
			t.Errorf("%s = %q, want %q", expr, stringify(v), want)
		}
	}
}

// A filter that is still unimplemented must FAIL, never render empty.
func TestAnUnimplementedFilterIsAnError(t *testing.T) {
	_, err := evalExpression("values.name | titlecase", map[string]any{})
	if err == nil {
		t.Fatal("an unknown filter rendered without error; silently emitting nothing is how " +
			"a scaffolded app ends up with syntactically invalid source")
	}
	if !strings.Contains(err.Error(), "dump") {
		t.Errorf("the error should name the supported filters, got %q", err)
	}
}

// A RepoUrlPicker host is BARE; skeletons add the scheme themselves.
func TestParseRepoURLReturnsABareHost(t *testing.T) {
	for _, in := range []string{
		"https://gitea.platform.adhar.io?owner=adhar&repo=reactapp",
		"http://gitea.platform.adhar.io/?owner=adhar&repo=reactapp.git",
		"gitea.platform.adhar.io?owner=adhar&repo=reactapp",
	} {
		host, owner, repo := parseRepoURL(in)
		if strings.Contains(host, "://") {
			t.Errorf("%s: scheme leaked into host %q, which doubles it in 'https://<host>/…'", in, host)
		}
		if host != "gitea.platform.adhar.io" || owner != "adhar" || repo != "reactapp" {
			t.Errorf("%s -> host=%q owner=%q repo=%q", in, host, owner, repo)
		}
	}
}

// `x or <default>` is Nunjucks's default-value idiom; every shipped template
// uses it for optional parameters. Falling through to a path lookup rendered
// it EMPTY, which is how a scaffolded Spring Boot service shipped a Kafka topic
// with no name: the Messaging XR composed `topic-<no value>`, an invalid name
// that fails the whole XR, and the service died at startup on "No resolvable
// bootstrap urls" with nothing pointing at the template.
func TestOrTakesTheDefaultOnlyWhenTheParameterIsMissing(t *testing.T) {
	empty := map[string]any{"parameters": map[string]any{}}
	set := map[string]any{"parameters": map[string]any{"kafkaTopic": "orders", "kafkaPartitions": 12, "cacheTtl": ""}}
	for _, tc := range []struct {
		expr string
		ctx  map[string]any
		want string
	}{
		{"parameters.kafkaTopic or 'events'", empty, "events"},
		{"parameters.kafkaPartitions or 3", empty, "3"},
		{"parameters.kafkaTopic or 'events'", set, "orders"},
		{"parameters.kafkaPartitions or 3", set, "12"},
		{"parameters.cacheTtl or '10m'", set, "10m"}, // "" is falsy, as in Nunjucks
	} {
		v, err := evalExpression(tc.expr, tc.ctx)
		if err != nil {
			t.Fatalf("%s: %v", tc.expr, err)
		}
		if stringify(v) != tc.want {
			t.Errorf("%s = %q, want %q", tc.expr, stringify(v), tc.want)
		}
	}
	// Other operators are still refused, never rendered as "".
	if _, err := evalExpression("parameters.a and 'b'", empty); err == nil {
		t.Error("`and` rendered without error; an unimplemented operator must fail loudly")
	}
}

// `| escape` is HTML escaping. The react-spa-app and angular-spa-app
// skeletons drop the description into `<meta name="description"
// content="${{ values.description | escape }}">`, where a double quote ends
// the attribute and `<` opens a tag — so an unescaped description is not a
// cosmetic problem, it is a different HTML document. Before this filter
// existed the CLI refused the expression outright and could not scaffold
// either SPA template.
func TestEscapeFilterHTMLEscapesForAttributes(t *testing.T) {
	ctx := map[string]any{"values": map[string]any{"description": `Tom's "fast" <app> & more`}}
	got, err := evalExpression("values.description | escape", ctx)
	if err != nil {
		t.Fatalf("escape returned an error: %v", err)
	}
	want := "Tom&#39;s &#34;fast&#34; &lt;app&gt; &amp; more"
	if stringify(got) != want {
		t.Errorf("escape rendered %q, want %q", stringify(got), want)
	}
	if strings.ContainsAny(stringify(got), `"<>`) {
		t.Errorf("escaped output still carries an attribute-breaking character: %q", stringify(got))
	}
}
