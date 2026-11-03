/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package apps

// Backstage software-template scaffolding for `adhar application deploy --template`.
//
// The platform's golden paths live in `adhar/adhar-templates` (mirrored from
// github.com/adhar-io/adhar-templates by the `adhar-libraries` package) as
// Backstage `scaffolder.backstage.io/v1beta3` Templates: a `template.yaml`
// declaring parameters and steps, next to a `skeleton/` tree the `fetch:template`
// step renders. Instantiating one therefore means rendering that skeleton into a
// new repository — NOT applying the template file itself, which is a Template
// entity and not a deployable resource.
//
// Only the templating the shipped skeletons actually use is implemented, and
// that is deliberate: `${{ values.<key> }}` substitution, `{% if %}/{% elif %}/
// {% else %}/{% endif %}` conditionals (paths, literals, ==, !=, not, and, or,
// parentheses) with the whitespace-trimming `{%- … -%}` form, and the
// `input.values` mapping that derives the render context from the parameters.
// This mirrors the Console's renderer (apps/console/app/server/
// template-render.ts) so the CLI and the Console produce the same repository
// from the same template — byte for byte across the shipped templates — but it
// is not a general Nunjucks engine, and a template that reaches past the subset
// fails loudly rather than emitting half-rendered files.

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"adhar-io/adhar/globals"

	"code.gitea.io/sdk/gitea"
	"encoding/json"
	"gopkg.in/yaml.v3"
)

// backstageTemplate is the subset of a scaffolder Template this CLI reads.
type backstageTemplate struct {
	Metadata struct {
		Name        string `yaml:"name"`
		Title       string `yaml:"title"`
		Description string `yaml:"description"`
		// The name and tags decide whether the template scaffolds a Function,
		// the way the Console decides it (isFunctionTemplate).
		Tags []string `yaml:"tags"`
		// adhar.io/dashboard-type and adhar.io/workload say what the scaffolded
		// Application is (see pavedRoadApplication).
		Labels map[string]string `yaml:"labels"`
	} `yaml:"metadata"`
	Spec struct {
		// The component type (service, website, function, …) a generated
		// catalog-info.yaml declares (consoleCatalogInfo).
		Type       string `yaml:"type"`
		Parameters []struct {
			Title      string                       `yaml:"title"`
			Required   []string                     `yaml:"required"`
			Properties map[string]templateParameter `yaml:"properties"`
		} `yaml:"parameters"`
		Steps []struct {
			ID     string `yaml:"id"`
			Name   string `yaml:"name"`
			Action string `yaml:"action"`
			Input  struct {
				URL    string               `yaml:"url"`
				Values map[string]yaml.Node `yaml:"values"`
				Path   string               `yaml:"path"`
			} `yaml:"input"`
		} `yaml:"steps"`
	} `yaml:"spec"`
}

type templateParameter struct {
	Title       string    `yaml:"title"`
	Type        string    `yaml:"type"`
	Description string    `yaml:"description"`
	Default     yaml.Node `yaml:"default"`
	Pattern     string    `yaml:"pattern"`
}

// fetchTemplateStep returns the `fetch:template` step, which is the one that
// names the skeleton and the render context.
func (t *backstageTemplate) fetchTemplateStep() (skeleton string, values map[string]yaml.Node, ok bool) {
	for _, s := range t.Spec.Steps {
		if s.Action == "fetch:template" {
			skeleton = strings.TrimPrefix(s.Input.URL, "./")
			if skeleton == "" {
				skeleton = "skeleton"
			}
			return skeleton, s.Input.Values, true
		}
	}
	return "", nil, false
}

// argoCDAppActions are the scaffolder actions that create the template's Argo
// CD Application. The shipped templates use the `adhar:` name; `cnoe:` is the
// upstream name older templates still carry.
var argoCDAppActions = map[string]bool{
	"adhar:create-argocd-app": true,
	"cnoe:create-argocd-app":  true,
}

// declaredManifestPath returns the directory the template's Argo CD step
// deploys from, if the template declares one.
func (t *backstageTemplate) declaredManifestPath() (string, bool) {
	for _, s := range t.Spec.Steps {
		if argoCDAppActions[s.Action] && s.Input.Path != "" {
			return s.Input.Path, true
		}
	}
	return "", false
}

// manifestPath returns the directory the template's Argo CD step deploys from,
// defaulting to the platform convention.
func (t *backstageTemplate) manifestPath() string {
	if p, ok := t.declaredManifestPath(); ok {
		return p
	}
	return "deploy"
}

// manifestPathFor is manifestPath for a rendered skeleton: when the template
// declares no Argo CD step, prefer a top-level `manifests/` directory if the
// skeleton has one (every shipped skeleton does) over the `deploy` default,
// so the Application never points at a directory that does not exist.
func (t *backstageTemplate) manifestPathFor(files []string) string {
	if p, ok := t.declaredManifestPath(); ok {
		return p
	}
	for _, f := range files {
		if strings.HasPrefix(f, "manifests/") {
			return "manifests"
		}
	}
	return "deploy"
}

// loadBackstageTemplate fetches and parses templates/<id>/template.yaml.
func loadBackstageTemplate(gc *gitea.Client, id string) (*backstageTemplate, error) {
	file := path.Join(globals.GitOpsTemplatesPath, id, "template.yaml")
	raw, _, err := gc.GetFile(globals.GiteaPlatformOrg, globals.GitOpsRepoTemplates, "main", file)
	if err != nil {
		avail := listGiteaTemplates(gc)
		if avail != "" {
			return nil, fmt.Errorf("template %q not found in gitea %s/%s. Available: %s",
				id, globals.GiteaPlatformOrg, globals.GitOpsRepoTemplates, avail)
		}
		return nil, fmt.Errorf("fetching template %q from gitea %s/%s (%s): %w",
			id, globals.GiteaPlatformOrg, globals.GitOpsRepoTemplates, file, err)
	}
	var t backstageTemplate
	if err := yaml.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", file, err)
	}
	return &t, nil
}

/* ───────────────────────── parameters ───────────────────────── */

// resolveParameters builds the `parameters` context: declared defaults first,
// then the caller's --param overrides, with `name` and the repo location always
// set so a template can be instantiated without naming them twice.
func resolveParameters(t *backstageTemplate, appName, repoOwner, repoName, host string, overrides map[string]string) (map[string]any, error) {
	params := map[string]any{}
	declared := map[string]templateParameter{}
	for _, group := range t.Spec.Parameters {
		for key, p := range group.Properties {
			declared[key] = p
			if !p.Default.IsZero() {
				var v any
				if err := p.Default.Decode(&v); err != nil {
					return nil, fmt.Errorf("decoding default for parameter %q: %w", key, err)
				}
				params[key] = v
			}
		}
	}

	params["name"] = appName
	// RepoUrlPicker values are `host?owner=X&repo=Y`; the skeletons parse them
	// back with `| parseRepoUrl`, so the CLI must produce the same shape.
	params["repoUrl"] = fmt.Sprintf("%s?owner=%s&repo=%s", host, url.QueryEscape(repoOwner), url.QueryEscape(repoName))

	for key, raw := range overrides {
		p, known := declared[key]
		if !known {
			return nil, fmt.Errorf("template %q has no parameter %q (declared: %s)",
				t.Metadata.Name, key, strings.Join(sortedKeys(declared), ", "))
		}
		v, err := coerceParameter(raw, p.Type)
		if err != nil {
			return nil, fmt.Errorf("parameter %q: %w", key, err)
		}
		params[key] = v
	}

	// Required parameters are checked after overrides so a missing one is
	// reported by name rather than surfacing as an empty substitution in a
	// rendered file, which is far harder to trace back.
	for _, group := range t.Spec.Parameters {
		for _, key := range group.Required {
			if v, ok := params[key]; !ok || v == nil || v == "" {
				return nil, fmt.Errorf("template %q requires parameter %q; pass --param %s=<value>",
					t.Metadata.Name, key, key)
			}
		}
	}
	return params, nil
}

func sortedKeys(m map[string]templateParameter) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// coerceParameter converts a --param string to the declared JSON-schema type, so
// `{% if values.withDatabase %}` sees a real boolean rather than the always-true
// string "false".
func coerceParameter(raw, typ string) (any, error) {
	switch typ {
	case "boolean":
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("%q is not a boolean", raw)
		}
		return b, nil
	case "integer":
		n, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("%q is not an integer", raw)
		}
		return n, nil
	case "number":
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not a number", raw)
		}
		return f, nil
	default:
		return raw, nil
	}
}

// buildValues evaluates the `fetch:template` step's input.values against the
// parameters, producing the `values` context the skeleton is rendered with.
func buildValues(spec map[string]yaml.Node, params map[string]any) (map[string]any, error) {
	ctx := map[string]any{"parameters": params}
	values := map[string]any{}
	for key, node := range spec {
		var raw any
		if err := node.Decode(&raw); err != nil {
			return nil, fmt.Errorf("decoding values.%s: %w", key, err)
		}
		s, isString := raw.(string)
		if !isString {
			values[key] = raw
			continue
		}
		// A value that is exactly one expression keeps its type (a boolean stays
		// a boolean); anything else is string interpolation.
		if expr, only := soleExpression(s); only {
			v, err := evalExpression(expr, ctx)
			if err != nil {
				return nil, fmt.Errorf("values.%s: %w", key, err)
			}
			values[key] = v
			continue
		}
		out, err := substitute(s, ctx)
		if err != nil {
			return nil, fmt.Errorf("values.%s: %w", key, err)
		}
		values[key] = out
	}
	return values, nil
}

/* ───────────────────────── rendering ───────────────────────── */

var (
	// `X | lower|upper|dump`
	filterRe = regexp.MustCompile(`^(.+?)\s*\|\s*(lower|upper|dump|escape)$`)
	// `X or Y`
	orRe = regexp.MustCompile(`^(.+?)\s+or\s+(.+)$`)
	// `( X or Y ) | filter`
	groupedFilterRe = regexp.MustCompile(`^\(\s*(.+)\s*\)\s*\|\s*(lower|upper|dump|escape)$`)
	// the right-hand side of `or`: 'str' / "str" / number / true / false
	quotedRe = regexp.MustCompile(`^'(.*)'$|^"(.*)"$`)
	numberRe = regexp.MustCompile(`^-?\d+(\.\d+)?$`)
	// operators we deliberately do not implement — they must fail, not render ""
	unsupportedOp = regexp.MustCompile(`\s(and|if|else|not|in)\s`)
	// A URL scheme prefix, stripped from a RepoUrlPicker host.
	schemeRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.\-]*://`)
	// ${{ … }}, as the Console's renderer matches it: at least one character,
	// and it may span lines.
	exprRe = regexp.MustCompile(`(?s)\$\{\{\s*(.+?)\s*\}\}`)
	// A value that is ONE expression and nothing else — the Console's
	// interpolateValue rule, /^\$\{\{\s*([\s\S]+?)\s*\}\}$/: no text around it,
	// not even a space, and it may span lines. Anything else is interpolated
	// into a string.
	soleRe = regexp.MustCompile(`^\$\{\{\s*([\s\S]+?)\s*\}\}$`)
	// (parameters.repoUrl | parseRepoUrl).owner
	parseRepoRe = regexp.MustCompile(`^\(\s*([A-Za-z0-9_.]+)\s*\|\s*parseRepoUrl\s*\)\.([A-Za-z]+)$`)
)

func soleExpression(s string) (string, bool) {
	m := soleRe.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// evalExpression evaluates the expression forms the shipped templates use.
func evalExpression(expr string, ctx map[string]any) (any, error) {
	expr = strings.TrimSpace(expr)
	if m := parseRepoRe.FindStringSubmatch(expr); m != nil {
		v, _ := lookupPath(m[1], ctx)
		host, owner, repo := parseRepoURL(fmt.Sprint(v))
		switch m[2] {
		case "host":
			return host, nil
		case "owner":
			return owner, nil
		case "repo":
			return repo, nil
		default:
			return nil, fmt.Errorf("parseRepoUrl has no field %q", m[2])
		}
	}
	// `X or Y` — Nunjucks's default-value idiom, used by every shipped template
	// for optional parameters: `${{ parameters.kafkaTopic or 'events' }}`. A
	// left-hand side that is missing, "", false or 0 takes the default, as in
	// the Console's renderer. Left unimplemented this fell through to a path
	// lookup of the whole expression and rendered EMPTY, which is how a
	// scaffolded service shipped a Kafka topic with no name (see
	// TestOrTakesTheDefault).
	// `( … ) | filter` — a grouped expression piped to a filter, which Nunjucks
	// needs because a filter binds tighter than `or`:
	//
	//	${{ (values.description or 'A React single-page app…') | dump }}
	//
	// Checked BEFORE `or` and evaluated inside-out. With only the `or` branch
	// the left-hand side became `(values.description`, the expression was
	// refused, and the scaffolder dropped App.jsx / app.component.ts from every
	// new SPA — the build then failed on a missing module, which looks like a
	// broken template rather than a renderer that cannot parse one line of it.
	if m := groupedFilterRe.FindStringSubmatch(expr); m != nil {
		inner, err := evalExpression(strings.TrimSpace(m[1]), ctx)
		if err != nil {
			return nil, err
		}
		return applyFilter(m[2], inner, m[1])
	}
	if m := orRe.FindStringSubmatch(expr); m != nil {
		// An unparseable left-hand side is an error, not a missing value: the
		// Console's renderer throws there too, so the two cannot disagree
		// about whether a file renders.
		lhs, err := evalExpression(strings.TrimSpace(m[1]), ctx)
		if err != nil {
			return nil, err
		}
		if orKeepsLeft(lhs) {
			return lhs, nil
		}
		return evalLiteralOrPath(strings.TrimSpace(m[2]), ctx)
	}
	// `X | lower` / `X | upper` / `X | dump`.
	//
	// `dump` is JSON encoding and the skeletons depend on it for values dropped
	// straight into source code — `const APP_NAME = ${{ values.name | dump }}`
	// needs the QUOTES. Refusing it outright meant this CLI could not render
	// the shipped react-spa-app template at all.
	if m := filterRe.FindStringSubmatch(expr); m != nil {
		v, _ := lookupPath(strings.TrimSpace(m[1]), ctx)
		return applyFilter(m[2], v, m[1])
	}
	if strings.ContainsAny(expr, "|()~+") || unsupportedOp.MatchString(expr) {
		return nil, fmt.Errorf("expression %q uses templating this CLI does not implement; supported: lower, upper, dump, escape, (x | parseRepoUrl).field, x or <default>", expr)
	}
	v, _ := lookupPath(expr, ctx)
	return v, nil
}

// parseRepoURL splits a Backstage RepoUrlPicker value (`host?owner=X&repo=Y`).
//
// `host` comes back BARE — no scheme, no trailing slash — because that is what
// the field means and what the skeletons assume; they add the scheme
// themselves (`'https://${{ values.giteaHost }}/...'`). Returning it verbatim
// gives every generated app a doubled scheme, which is what the Console's
// renderer did until 2026-10-13:
//
//	const REPO_URL = 'https://https://gitea.platform.adhar.io/adhar/reactapp'
func parseRepoURL(s string) (host, owner, repo string) {
	host, query, _ := strings.Cut(s, "?")
	host = schemeRe.ReplaceAllString(host, "")
	host = strings.TrimRight(host, "/")
	q, err := url.ParseQuery(query)
	if err != nil {
		return host, "", ""
	}
	return host, q.Get("owner"), strings.TrimSuffix(q.Get("repo"), ".git")
}

// lookupPath resolves a dotted path against the context. A bare key resolves
// against `values` when present, matching the renderer the Console uses.
func lookupPath(p string, ctx map[string]any) (any, bool) {
	parts := strings.Split(p, ".")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	var cur any = ctx
	if len(parts) == 1 {
		if _, direct := ctx[parts[0]]; !direct {
			if vals, ok := ctx["values"].(map[string]any); ok {
				cur = vals
			}
		}
	}
	for _, part := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// substitute replaces every ${{ … }} in s.
func substitute(s string, ctx map[string]any) (string, error) {
	var firstErr error
	out := exprRe.ReplaceAllStringFunc(s, func(m string) string {
		expr := exprRe.FindStringSubmatch(m)[1]
		v, err := evalExpression(expr, ctx)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return m
		}
		return stringify(v)
	})
	return out, firstErr
}

func stringify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case int:
		return strconv.Itoa(t)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case []any:
		// JavaScript's String([...]): the elements joined by commas, which is
		// what the Console's renderer writes for a list value.
		parts := make([]string, len(t))
		for i, e := range t {
			parts[i] = stringify(e)
		}
		return strings.Join(parts, ",")
	default:
		return fmt.Sprint(t)
	}
}

// truthy is the Console renderer's truthiness for `{% if %}` conditions
// (template-render.ts): absent, false, 0, and a string that is empty, "false",
// "0" or "no" (trimmed, any case) are false.
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "", "false", "0", "no":
			return false
		}
		return true
	case int:
		return t != 0
	case int64:
		return t != 0
	case float64:
		return t != 0
	default:
		return true
	}
}

// orKeepsLeft is the `x or <default>` rule of the Console's renderer: the left
// side stands unless it is missing, "", false or 0.
func orKeepsLeft(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case int:
		return t != 0
	case int64:
		return t != 0
	case float64:
		return t != 0
	default:
		return true
	}
}

var (
	// The whitespace-control markers, normalised away before conditionals are
	// resolved: `{%-` eats the whitespace before it, up to and including one
	// line break; `-%}` eats the whitespace after it, up to and including one.
	trimBeforeRe = regexp.MustCompile(`[ \t]*\r?\n?[ \t]*\{%-`)
	trimAfterRe  = regexp.MustCompile(`-%\}[ \t]*\r?\n?`)
	// {% if %} / {% elif %} / {% else %} / {% endif %}, after normalisation.
	condTagRe = regexp.MustCompile(`\{%\s*(if|elif|else|endif)\b([^%]*)%\}`)
	// The tokens of an {% if %} condition.
	condTokenRe = regexp.MustCompile(`\s*('(?:[^'\\]|\\.)*'|"(?:[^"\\]|\\.)*"|==|!=|\(|\)|[A-Za-z_][\w.]*|-?\d+(?:\.\d+)?|\S)`)
)

// renderContent renders one skeleton file exactly as the Console's renderer
// does (apps/console/app/server/template-render.ts renderContent), so the CLI
// and the Console commit the same repository from the same template:
// whitespace-control markers first, then {% if %}/{% elif %}/{% else %}/
// {% endif %} — so a substitution inside a discarded branch is never
// evaluated — then the ${{ … }} substitutions.
//
// Until 2026-11-03 this was a renderer of its own, and the two disagreed on
// a third of the shipped templates' files: it dropped the line break after
// EVERY tag, which fused lines wherever a tag closed mid-line (package.json's
// `"7.3.0"  },`, a README code fence that never closed), and it looked an
// operator up as a path, so `{%- if values.chaosTesting == false %}` was never
// true — a service scaffolded with chaosTesting=false still got its chaos
// experiments — and `{%- if not (values.withDatabase or …) %}` either failed
// the whole scaffold (springboot-web-service with its defaults) or rendered
// compose.yaml as `services: {}` followed by the services, which is not YAML
// (transaction-service).
func renderContent(src string, ctx map[string]any) (string, error) {
	s := trimBeforeRe.ReplaceAllString(src, "{%")
	s = strings.ReplaceAll(s, "{%-", "{%")
	s = trimAfterRe.ReplaceAllString(s, "%}")
	s = strings.ReplaceAll(s, "-%}", "%}")
	s, err := resolveConditionals(s, ctx)
	if err != nil {
		return "", err
	}
	return substitute(s, ctx)
}

type condTag struct {
	kind, cond string
	start, end int
}

// resolveConditionals resolves the if/elif/else/endif blocks, nested to any
// depth, by matching tags on a stack. A branch that is not taken is not
// evaluated — its conditions included — but its tags must still balance.
func resolveConditionals(s string, ctx map[string]any) (string, error) {
	locs := condTagRe.FindAllStringSubmatchIndex(s, -1)
	if len(locs) == 0 {
		return s, nil
	}
	tags := make([]condTag, len(locs))
	for i, l := range locs {
		tags[i] = condTag{kind: s[l[2]:l[3]], cond: s[l[4]:l[5]], start: l[0], end: l[1]}
	}
	k := 0
	// block renders s[from:] up to the first tag it did not open, which it
	// returns for the caller to interpret (elif/else/endif), or nil at the end.
	var block func(from int, out bool) (string, *condTag, error)
	block = func(from int, out bool) (string, *condTag, error) {
		var b strings.Builder
		pos := from
		for k < len(tags) {
			t := tags[k]
			if out {
				b.WriteString(s[pos:t.start])
			}
			if t.kind != "if" {
				return b.String(), &tags[k], nil
			}
			k++
			taken, take := false, false
			if out {
				var err error
				if take, err = evalCondition(t.cond, ctx); err != nil {
					return "", nil, err
				}
			}
			next := t.end
			for {
				body, closer, err := block(next, take)
				if err != nil {
					return "", nil, err
				}
				if take {
					b.WriteString(body)
					taken = true
				}
				if closer == nil {
					return "", nil, fmt.Errorf("unclosed {%% if %%} at offset %d", t.start)
				}
				k++
				next = closer.end
				if closer.kind == "endif" {
					break
				}
				take = out && !taken
				if take && closer.kind == "elif" {
					if take, err = evalCondition(closer.cond, ctx); err != nil {
						return "", nil, err
					}
				}
			}
			pos = next
		}
		if out {
			b.WriteString(s[pos:])
		}
		return b.String(), nil, nil
	}
	text, stray, err := block(0, true)
	if err != nil {
		return "", err
	}
	if stray != nil {
		return "", fmt.Errorf("{%% %s %%} at offset %d has no matching {%% if %%}", stray.kind, stray.start)
	}
	return text, nil
}

// evalCondition evaluates an {% if %} condition the way the Console's
// renderer does: paths, quoted strings, numbers, true/false/none, == and !=
// (compared as rendered text), not, and, or and parentheses, with Nunjucks
// precedence (not > and > or). Anything else is an error, never a guess.
func evalCondition(cond string, ctx map[string]any) (bool, error) {
	raw := condTokenRe.FindAllString(cond, -1)
	toks := make([]string, len(raw))
	for i, t := range raw {
		toks[i] = strings.TrimSpace(t)
	}
	fail := func(why string) error {
		return fmt.Errorf("template condition %q: %s; supported: paths, literals, ==, !=, not, and, or, ( )",
			strings.TrimSpace(cond), why)
	}
	i := 0
	peek := func() string {
		if i < len(toks) {
			return toks[i]
		}
		return ""
	}
	var orExpr func() (bool, error)
	atom := func() (any, error) {
		if i >= len(toks) {
			return nil, fail("ends early")
		}
		t := toks[i]
		i++
		switch {
		case t == "(":
			v, err := orExpr()
			if err != nil {
				return nil, err
			}
			if peek() != ")" {
				return nil, fail("unbalanced parentheses")
			}
			i++
			return v, nil
		case strings.HasPrefix(t, "'") || strings.HasPrefix(t, `"`):
			return t[1 : len(t)-1], nil
		case numberRe.MatchString(t):
			f, _ := strconv.ParseFloat(t, 64)
			return f, nil
		case t == "true" || t == "True":
			return true, nil
		case t == "false" || t == "False":
			return false, nil
		case t == "none" || t == "null" || t == "None":
			return nil, nil
		}
		switch t {
		case "and", "or", "not", "in", "is", ")", "==", "!=":
			return nil, fail(fmt.Sprintf("unexpected %q", t))
		}
		if c := t[0]; !(c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
			return nil, fail(fmt.Sprintf("unexpected %q", t))
		}
		v, _ := lookupPath(t, ctx)
		return v, nil
	}
	cmp := func() (any, error) {
		left, err := atom()
		if err != nil {
			return nil, err
		}
		if op := peek(); op == "==" || op == "!=" {
			i++
			right, err := atom()
			if err != nil {
				return nil, err
			}
			eq := stringify(left) == stringify(right)
			return eq == (op == "=="), nil
		}
		return left, nil
	}
	var notExpr func() (bool, error)
	notExpr = func() (bool, error) {
		if peek() == "not" {
			i++
			v, err := notExpr()
			return !v, err
		}
		v, err := cmp()
		return truthy(v), err
	}
	andExpr := func() (bool, error) {
		v, err := notExpr()
		for err == nil && peek() == "and" {
			i++
			var r bool
			r, err = notExpr()
			v = v && r
		}
		return v, err
	}
	orExpr = func() (bool, error) {
		v, err := andExpr()
		for err == nil && peek() == "or" {
			i++
			var r bool
			r, err = andExpr()
			v = v || r
		}
		return v, err
	}
	if len(toks) == 0 {
		return false, fail("is empty")
	}
	res, err := orExpr()
	if err != nil {
		return false, err
	}
	if i != len(toks) {
		return false, fail(fmt.Sprintf("unexpected %q", toks[i]))
	}
	return res, nil
}

/* ───────────────────────── orchestration ───────────────────────── */

// scaffoldedRepo is what a rendered template produced.
type scaffoldedRepo struct {
	CloneURL     string
	ManifestPath string
	Files        int
	// TemplateName, TemplateTags and TemplateLabels are the template's own
	// metadata; they say what the scaffolded Application is
	// (pavedRoadApplication).
	TemplateName   string
	TemplateTags   []string
	TemplateLabels map[string]string
}

// renderedScaffold is a template's skeleton rendered in memory: nothing has
// been written anywhere yet.
type renderedScaffold struct {
	template   *backstageTemplate
	templateID string
	files      []*gitea.ChangeFileOperation
	paths      []string
	// description is the repository's description: the application's own
	// `description` parameter, empty when none was given — what the Console
	// creates the repository with (body.description ?? ''), not the
	// template's description of itself.
	description string
}

// scaffoldGiteaBase is the platform Gitea's base URL as the Console writes it:
// the scheme's default port dropped (https://gitea.<domain>, not
// https://gitea.<domain>:443), any other port kept (a laptop's :8443).
//
// utils.GiteaBaseUrl always spells the port, and every skeleton builds its
// links from the RepoUrlPicker host (`https://${{ values.giteaHost }}/…`), so
// until 2026-11-03 a CLI-scaffolded repository differed from a
// Console-scaffolded one in the catalog's docs link, the SPA's REPO_URL and —
// through the .adhar/app.yaml descriptor Argo CD adopts — the Application's
// own source.repoURL and the links on its Grafana dashboard (reactapp).
func scaffoldGiteaBase(giteaURL string) string {
	return strings.TrimSuffix(publicCloneURL(strings.TrimSuffix(giteaURL, "/")), "/")
}

// scaffoldContext is the context a template's skeleton is rendered with: the
// resolved parameters, the fetch:template values, and the values the
// scaffolder pins — the same ones the Console's scaffolder pins
// (apps/console/app/server/scaffolder.ts), so both produce one repository
// from one template. It touches nothing, so that parity can be tested.
func scaffoldContext(t *backstageTemplate, valueSpec map[string]yaml.Node, giteaURL, appName, env string, overrides map[string]string) (map[string]any, error) {
	host := "gitea"
	if u, uerr := url.Parse(scaffoldGiteaBase(giteaURL)); uerr == nil && u.Host != "" {
		host = u.Host
	}
	params, err := resolveParameters(t, appName, globals.GiteaPlatformOrg, appName, host, overrides)
	if err != nil {
		return nil, err
	}
	// The Console always passes a description, empty when none was given
	// (params.description = body.description ?? ''), so a skeleton that dumps
	// it writes "" there; left unset here it rendered null.
	if params["description"] == nil {
		params["description"] = ""
	}
	values, err := buildValues(valueSpec, params)
	if err != nil {
		return nil, err
	}
	// The repository identity is the repository the scaffold creates, not
	// whatever a template derives from the picker value.
	values["gitOwner"] = globals.GiteaPlatformOrg
	values["repoName"] = appName
	// Pin the public hostname to THIS platform's domain.
	//
	// The shipped templates compute their hostname as
	// `<name>.adhar.localtest.me` — the local-development domain. On a real
	// install that produced an HTTPRoute for a hostname the cluster does not
	// serve, so the scaffolded service came with a URL that could never resolve.
	// A template cannot know the domain; the scaffolder can, so it decides. The
	// Console's scaffolder pins the same values the same way, which is what
	// keeps a CLI-scaffolded repo and a Console-scaffolded repo identical.
	base := platformBaseDomain(host)
	if base != "" {
		values["platformBaseDomain"] = base
		// The ENVIRONMENT is part of the hostname: <app>.<env>.<base>.
		//
		// Without it every environment of every app competes for one name.
		// `my-service.<base>` cannot simultaneously be the dev copy and the
		// prod copy, so the promotion chain this platform is built around
		// (dev -> test -> prod, prod held for a person) had nowhere to put the
		// other two — the ml template's own comment says as much: "give each
		// environment its own HTTPRoute hostname, since three namespaces cannot
		// share one".
		//
		// This also means the Gateway must terminate TLS for `*.<env>.<base>`,
		// which a `*.<base>` wildcard does NOT cover — a TLS wildcard matches
		// exactly one label. The per-environment listeners in
		// resources/gateway/gateway-cloud.yaml exist for that, and
		// cert-manager's gateway support extends the certificate from them.
		// Changing this line without those listeners gives every new app a URL
		// that resolves and then serves the wrong certificate.
		values["environment"] = env
		values["hostname"] = platformRouteHost(appName, env, base)
	}
	// Every hostname the HTTPRoute serves, as JSON: the templates substitute it
	// as a YAML flow sequence (`hostnames: ${{ values.hostnamesJson }}`). The
	// platform route is always first; `--set customHostnames=a.example.org,b`
	// adds the team's own on top (scaffold_hosts.go).
	values["hostnamesJson"] = routeHostnamesJSON(stringify(values["hostname"]), "")
	// One list per environment for the dev/test/prod overlays. The platform
	// route is <app>.<env>.<base>; custom hostnames are a production concern
	// and go on prod only.
	for _, e := range []string{"dev", "test", "prod"} {
		h := envHostname(stringify(values["hostname"]), env, e)
		if base != "" {
			h = platformRouteHost(appName, e, base)
		}
		custom := ""
		if e == "prod" {
			custom = stringify(params["customHostnames"])
		}
		values[e+"HostnamesJson"] = routeHostnamesJSON(h, custom)
	}
	return map[string]any{"values": values, "parameters": params}, nil
}

// renderScaffold renders a template's skeleton in memory.
//
// Everything is rendered BEFORE the repository is created: a skeleton this
// renderer cannot render used to leave an empty repository behind (the
// repository was created first), which the next attempt then adopted only to
// fail the same way.
func renderScaffold(ctx context.Context, gc *gitea.Client, t *backstageTemplate, giteaURL, templateID, appName, env string, overrides map[string]string) (*renderedScaffold, error) {
	skeleton, valueSpec, ok := t.fetchTemplateStep()
	if !ok {
		return nil, fmt.Errorf("template %q has no fetch:template step, so there is no skeleton to render", templateID)
	}
	ctxVals, err := scaffoldContext(t, valueSpec, giteaURL, appName, env, overrides)
	if err != nil {
		return nil, err
	}

	tree, _, err := gc.GetTrees(globals.GiteaPlatformOrg, globals.GitOpsRepoTemplates, gitea.ListTreeOptions{
		Ref:       "main",
		Recursive: true,
		ListOptions: gitea.ListOptions{
			PageSize: 1000,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("listing the %s repo tree: %w", globals.GitOpsRepoTemplates, err)
	}
	prefix := path.Join(globals.GitOpsTemplatesPath, templateID, skeleton) + "/"
	var entries []gitea.GitEntry
	for _, e := range tree.Entries {
		if e.Type == "blob" && strings.HasPrefix(e.Path, prefix) {
			entries = append(entries, e)
		}
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("template %q has no files under %s", templateID, prefix)
	}
	if tree.Truncated {
		return nil, fmt.Errorf("the %s repo tree was truncated; cannot render %q completely",
			globals.GitOpsRepoTemplates, templateID)
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	params, _ := ctxVals["parameters"].(map[string]any)
	description, _ := params["description"].(string)
	r := &renderedScaffold{template: t, templateID: templateID, description: description}
	for _, e := range entries {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		raw, _, gerr := gc.GetFile(globals.GiteaPlatformOrg, globals.GitOpsRepoTemplates, "main", e.Path)
		if gerr != nil {
			return nil, fmt.Errorf("reading %s: %w", e.Path, gerr)
		}
		rel, content, rerr := renderSkeletonFile(strings.TrimPrefix(e.Path, prefix), raw, skeletonRenderContext(ctxVals))
		if rerr != nil {
			return nil, fmt.Errorf("rendering %s: %w", e.Path, rerr)
		}
		r.add(rel, content)
	}
	repoURL := fmt.Sprintf("%s/%s/%s", scaffoldGiteaBase(giteaURL), globals.GiteaPlatformOrg, appName)
	for _, f := range consoleFallbacks(t, r.paths, appName, repoURL, params) {
		r.add(f.path, []byte(f.content))
	}
	return r, nil
}

func (r *renderedScaffold) add(rel string, content []byte) {
	r.files = append(r.files, &gitea.ChangeFileOperation{
		Operation: "create",
		Path:      rel,
		Content:   base64.StdEncoding.EncodeToString(content),
	})
	r.paths = append(r.paths, rel)
}

// consoleStarterKustomization is the deploy starter the Console's scaffolder
// commits (scaffolder.ts, step 3).
const consoleStarterKustomization = "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: []\n"

type fallbackFile struct{ path, content string }

// consoleFallbacks are the files the Console's scaffolder adds when a skeleton
// does not ship them: a generated catalog-info.yaml (step 2b), and an empty
// Kustomization under the manifest path when nothing landed there (step 3),
// so the Application's source is never a directory that does not exist. Every
// shipped template carries both; one that does not must still produce the
// repository the Console would — here in the scaffold's one commit, where the
// Console makes each its own.
func consoleFallbacks(t *backstageTemplate, paths []string, appName, repoURL string, params map[string]any) []fallbackFile {
	var out []fallbackFile
	if !contains(paths, "catalog-info.yaml") {
		out = append(out, fallbackFile{"catalog-info.yaml", consoleCatalogInfo(t, appName, repoURL, params)})
	}
	manifests := t.manifestPathFor(paths)
	for _, p := range paths {
		if strings.HasPrefix(p, manifests+"/") {
			return out
		}
	}
	return append(out, fallbackFile{manifests + "/kustomization.yaml", consoleStarterKustomization})
}

// consoleCatalogInfo is the descriptor the Console's buildCatalogInfo writes
// (scaffolder.ts) for a template scaffold: what the Create wizard sends — its
// title, description, owner, system, domain and lifecycle fields, which are
// the template's parameters of those names, the template's spec.type and its
// tags — the repository, and, a template scaffold being GitOps, the Argo CD
// app name.
func consoleCatalogInfo(t *backstageTemplate, appName, repoURL string, params map[string]any) string {
	str := func(key string) string {
		s, _ := params[key].(string)
		return s
	}
	or := func(v, def string) string {
		if v == "" {
			return def
		}
		return v
	}
	tags := t.Metadata.Tags
	if len(tags) == 0 {
		// The wizard's copy of a template without tags (templates.ts).
		tags = []string{"gitea"}
	}
	quoted := make([]string, len(tags))
	for i, tag := range tags {
		quoted[i] = jsString(tag)
	}
	lines := []string{"apiVersion: backstage.io/v1alpha1", "kind: Component", "metadata:", "  name: " + appName}
	if v := str("title"); v != "" {
		lines = append(lines, "  title: "+jsString(v))
	}
	if v := str("description"); v != "" {
		lines = append(lines, "  description: "+jsString(v))
	}
	lines = append(lines,
		"  annotations:",
		"    adhar.io/git-repo: "+repoURL,
		"    argocd/app-name: "+appName,
		"  tags: ["+strings.Join(quoted, ", ")+"]",
		"spec:",
		"  type: "+or(t.Spec.Type, "service"),
		"  lifecycle: "+or(str("lifecycle"), "experimental"),
		"  owner: "+or(str("owner"), "group:platform"),
	)
	if v := str("system"); v != "" {
		lines = append(lines, "  system: "+v)
	}
	if v := str("domain"); v != "" {
		lines = append(lines, "  domain: "+v)
	}
	return strings.Join(lines, "\n") + "\n"
}

// jsString is JavaScript's JSON.stringify of a string.
func jsString(s string) string {
	out, _ := jsJSON(s)
	return out
}

// jsJSON is JavaScript's JSON.stringify of a value the renderer produced.
// encoding/json differs in two ways: by default it escapes <, > and &, and it
// always escapes U+2028 and U+2029 (and writes invalid UTF-8 as �), where
// JSON.stringify writes those characters as they are.
func jsJSON(v any) (string, error) {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	out := strings.TrimSuffix(b.String(), "\n")
	if !strings.Contains(out, ` `) && !strings.Contains(out, ` `) && !strings.Contains(out, `�`) {
		return out, nil
	}
	// Undo just those escapes. Backslashes appear only inside strings, each
	// starting an escape: `\uXXXX` or a two-character one such as `\\`, so an
	// escaped backslash followed by "u2028" is left alone.
	var r strings.Builder
	for i := 0; i < len(out); i++ {
		if out[i] != '\\' || i+1 >= len(out) {
			r.WriteByte(out[i])
			continue
		}
		if out[i+1] == 'u' && i+6 <= len(out) {
			switch out[i+2 : i+6] {
			case "2028", "2029", "fffd":
				n, _ := strconv.ParseUint(out[i+2:i+6], 16, 32)
				r.WriteRune(rune(n))
				i += 5
				continue
			}
		}
		r.WriteString(out[i : i+2])
		i++
	}
	return r.String(), nil
}

// htmlEscaper is the Console renderer's escapeHtml. html.EscapeString writes a
// double quote as &#34; where the Console writes &quot;.
var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")

// skeletonRenderContext is the context a skeleton FILE is rendered with: the
// values and nothing else, each also under its own name — the Console's
// `{ values, ...values }` (template-render.ts renderContent/renderPath). The
// parameters are not in it: they reach a skeleton only through the
// fetch:template values map, as in Backstage. With them in, a skeleton
// writing `${{ parameters.port }}` rendered the port here and nothing in the
// Console, so the two scaffolders committed different files.
func skeletonRenderContext(scaffoldCtx map[string]any) map[string]any {
	values, _ := scaffoldCtx["values"].(map[string]any)
	out := make(map[string]any, len(values)+1)
	for k, v := range values {
		out[k] = v
	}
	out["values"] = values
	return out
}

// renderSkeletonFile renders one skeleton file the way the Console's
// scaffolder does: the path's ${{ … }} substituted (a path may be templated,
// `${{ values.name }}.go`), the content rendered unless it is binary.
func renderSkeletonFile(rel string, raw []byte, ctxVals map[string]any) (string, []byte, error) {
	out, err := substitute(rel, ctxVals)
	if err != nil {
		return "", nil, fmt.Errorf("its path: %w", err)
	}
	if !isTextTemplate(out) {
		return out, raw, nil
	}
	rendered, err := renderContent(string(raw), ctxVals)
	if err != nil {
		return "", nil, err
	}
	return out, []byte(rendered), nil
}

// commitScaffold creates the application's repository in the platform org and
// commits the rendered skeleton to it, so the generated service is editable in
// git from its first commit — the point of a golden path.
func commitScaffold(gc *gitea.Client, giteaURL, appName string, r *renderedScaffold) (*scaffoldedRepo, error) {
	repo, err := ensureScaffoldRepo(gc, appName, r.description)
	if err != nil {
		return nil, err
	}
	// ONE commit for the whole skeleton. Committing file by file made a
	// 14-file template 14 commits, and the repository's push webhook started
	// the app-ci pipeline 14 times in parallel — for a repository that was not
	// even complete until the last one. A scaffold is one change.
	if _, _, cerr := gc.ChangeFiles(globals.GiteaPlatformOrg, repo, gitea.ChangeFilesOptions{
		Files:   r.files,
		Message: fmt.Sprintf("scaffold %s from %s", appName, r.templateID),
		Branch:  "main",
	}); cerr != nil {
		return nil, fmt.Errorf("committing %d files to %s/%s: %w", len(r.files), globals.GiteaPlatformOrg, repo, cerr)
	}

	return &scaffoldedRepo{
		CloneURL:       fmt.Sprintf("%s/%s/%s.git", scaffoldGiteaBase(giteaURL), globals.GiteaPlatformOrg, repo),
		ManifestPath:   r.template.manifestPathFor(r.paths),
		Files:          len(r.files),
		TemplateName:   r.template.Metadata.Name,
		TemplateTags:   r.template.Metadata.Tags,
		TemplateLabels: r.template.Metadata.Labels,
	}, nil
}

// binaryExt are the extensions committed verbatim: substituting into a PNG
// corrupts it. The Console's list (BINARY_EXT in scaffolder.ts), SVG included,
// plus a few more that are never text.
var binaryExt = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".ico": true,
	".webp": true, ".bmp": true, ".svg": true, ".woff": true, ".woff2": true, ".ttf": true,
	".otf": true, ".eot": true, ".pdf": true, ".zip": true, ".gz": true,
	".tar": true, ".jar": true, ".class": true, ".wasm": true, ".mp4": true,
	".mp3": true, ".so": true, ".dylib": true, ".exe": true,
}

func isTextTemplate(p string) bool {
	return !binaryExt[strings.ToLower(path.Ext(p))]
}

// ensureScaffoldRepo creates the application's repository, refusing to scaffold
// over one that already has content — a half-overwritten repo is worse than a
// clear error.
func ensureScaffoldRepo(gc *gitea.Client, name, description string) (string, error) {
	existing, _, err := gc.GetRepo(globals.GiteaPlatformOrg, name)
	if err == nil && existing != nil {
		if !existing.Empty {
			return "", fmt.Errorf("repository %s/%s already exists and is not empty; delete it or choose another application name",
				globals.GiteaPlatformOrg, name)
		}
		return name, nil
	}
	repo, _, err := gc.CreateOrgRepo(globals.GiteaPlatformOrg, gitea.CreateRepoOption{
		Name:          name,
		Description:   description,
		Private:       false,
		DefaultBranch: "main",
		// No auto-init: an initial commit would leave a README the template did
		// not ask for, and CreateFile creates the branch on the first file.
		AutoInit: false,
	})
	if err != nil {
		return "", fmt.Errorf("creating repository %s/%s: %w", globals.GiteaPlatformOrg, name, err)
	}
	return repo.Name, nil
}

// platformBaseDomain derives the platform's base domain from the Gitea host.
//
// Gitea is published at `gitea.<base>`, so dropping the first label yields the
// domain every other platform service is published under. The port is stripped
// because a Gateway API hostname must not carry one — a laptop install publishes
// on :8443 and a route declaring it would never match.
//
// Returns "" when the host is not of that shape, and the caller then leaves the
// template's own hostname alone rather than substituting a guess.
func platformBaseDomain(giteaHost string) string {
	h := giteaHost
	if i := strings.LastIndex(h, ":"); i > 0 {
		h = h[:i]
	}
	parts := strings.SplitN(h, ".", 2)
	if len(parts) != 2 || parts[1] == "" || !strings.Contains(parts[1], ".") {
		return ""
	}
	return parts[1]
}

// evalLiteralOrPath evaluates the right-hand side of `or`: a quoted string, a
// number, a boolean, or another context path.
func evalLiteralOrPath(raw string, ctx map[string]any) (any, error) {
	if m := quotedRe.FindStringSubmatch(raw); m != nil {
		if m[1] != "" || strings.HasPrefix(raw, "'") {
			return m[1], nil
		}
		return m[2], nil
	}
	if numberRe.MatchString(raw) {
		if strings.Contains(raw, ".") {
			f, err := strconv.ParseFloat(raw, 64)
			return f, err
		}
		n, err := strconv.Atoi(raw)
		return n, err
	}
	switch raw {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null", "none", "None":
		return nil, nil
	}
	return evalExpression(raw, ctx)
}

// applyFilter applies one of the four implemented filters to an evaluated value.
func applyFilter(name string, v any, src string) (any, error) {
	switch name {
	case "dump":
		// JSON.stringify, as the Console's renderer dumps. encoding/json's
		// default escapes &, < and > (and U+2028/U+2029), so a description
		// such as "Orders & payments" was committed as "Orders &
		// payments" in package.json, catalog-info.yaml and mkdocs.yml.
		s, err := jsJSON(v)
		if err != nil {
			return nil, fmt.Errorf("dumping %s: %w", src, err)
		}
		return s, nil
	case "escape":
		// HTML escaping: the SPA skeletons drop the description into a
		// `<meta content="...">` attribute, where a quote ends the value.
		return htmlEscaper.Replace(stringify(v)), nil
	case "lower":
		return strings.ToLower(stringify(v)), nil
	default:
		return strings.ToUpper(stringify(v)), nil
	}
}
