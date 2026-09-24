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
// that is deliberate: `${{ values.<key> }}` substitution, `{% if %}/{% else %}/
// {% endif %}` conditionals including the whitespace-trimming `{%- … -%}` form,
// and the `input.values` mapping that derives the render context from the
// parameters. This mirrors the Console's renderer (apps/console/app/server/
// template-render.ts) so the CLI and the Console produce the same repository
// from the same template; it is not a general Nunjucks engine, and a template
// that reaches past the subset fails loudly rather than emitting half-rendered
// files.

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
	"gopkg.in/yaml.v3"
)

// backstageTemplate is the subset of a scaffolder Template this CLI reads.
type backstageTemplate struct {
	Metadata struct {
		Name        string `yaml:"name"`
		Title       string `yaml:"title"`
		Description string `yaml:"description"`
	} `yaml:"metadata"`
	Spec struct {
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

// manifestPath returns the directory the template's Argo CD step deploys from,
// defaulting to the platform convention.
func (t *backstageTemplate) manifestPath() string {
	for _, s := range t.Spec.Steps {
		if s.Action == "cnoe:create-argocd-app" && s.Input.Path != "" {
			return s.Input.Path
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
	exprRe = regexp.MustCompile(`\$\{\{(.*?)\}\}`)
	// {% if … %} / {% else %} / {% endif %}, with the trimming {%- and -%} forms.
	tagRe  = regexp.MustCompile(`\{%-?\s*(if|else|endif)\b([^%]*?)-?%\}`)
	soleRe = regexp.MustCompile(`^\s*\$\{\{(.*?)\}\}\s*$`)
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
	if strings.ContainsAny(expr, "|()") {
		return nil, fmt.Errorf("expression %q uses templating this CLI does not implement; instantiate this template from the Console", expr)
	}
	v, _ := lookupPath(expr, ctx)
	return v, nil
}

// parseRepoURL splits a Backstage RepoUrlPicker value (`host?owner=X&repo=Y`).
func parseRepoURL(s string) (host, owner, repo string) {
	host, query, _ := strings.Cut(s, "?")
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
	default:
		return fmt.Sprint(t)
	}
}

// truthy follows Nunjucks/JS truthiness, which is what the skeleton conditionals
// were written against: 0, "", false and absent are false.
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != "" && t != "false"
	case int:
		return t != 0
	case float64:
		return t != 0
	default:
		return true
	}
}

// renderContent renders one skeleton file: conditionals first (so a substitution
// inside a discarded branch is never evaluated), then substitutions.
func renderContent(src string, ctx map[string]any) (string, error) {
	out, err := renderConditionals(src, ctx)
	if err != nil {
		return "", err
	}
	return substitute(out, ctx)
}

// renderConditionals evaluates {% if %}/{% else %}/{% endif %}, nesting included.
func renderConditionals(src string, ctx map[string]any) (string, error) {
	type frame struct {
		keep    bool // the branch currently being read is kept
		matched bool // the if-branch was taken (so else is not)
		emitted bool // output position of this frame's parent is emitting
	}
	var b strings.Builder
	var stack []frame
	emitting := func() bool {
		for _, f := range stack {
			if !f.keep {
				return false
			}
		}
		return true
	}

	pos := 0
	for _, loc := range tagRe.FindAllStringSubmatchIndex(src, -1) {
		if emitting() {
			b.WriteString(src[pos:loc[0]])
		}
		pos = loc[1]
		tag := src[loc[2]:loc[3]]
		arg := strings.TrimSpace(src[loc[4]:loc[5]])
		// A tag on its own line leaves a blank line behind; drop the newline that
		// immediately follows it, which is what the `-%}` form asks for anyway.
		if pos < len(src) && src[pos] == '\n' {
			pos++
		}
		switch tag {
		case "if":
			v, err := evalExpression(arg, ctx)
			if err != nil {
				return "", err
			}
			keep := truthy(v)
			stack = append(stack, frame{keep: keep, matched: keep})
		case "else":
			if len(stack) == 0 {
				return "", fmt.Errorf("{%% else %%} outside an {%% if %%}")
			}
			top := &stack[len(stack)-1]
			top.keep = !top.matched
		case "endif":
			if len(stack) == 0 {
				return "", fmt.Errorf("{%% endif %%} outside an {%% if %%}")
			}
			stack = stack[:len(stack)-1]
		}
	}
	if len(stack) != 0 {
		return "", fmt.Errorf("unclosed {%% if %%} in template")
	}
	b.WriteString(src[pos:])
	return b.String(), nil
}

/* ───────────────────────── orchestration ───────────────────────── */

// scaffoldedRepo is what a rendered template produced.
type scaffoldedRepo struct {
	CloneURL     string
	ManifestPath string
	Files        int
}

// scaffoldTemplate renders a template's skeleton into a new Gitea repository
// owned by the platform org and returns where the manifests landed, so the
// caller can hand the repo to the same CompositeApplication path a --repo deploy
// takes. The application's source therefore lives in git from the first commit,
// which is the point of a golden path: the generated service is editable.
func scaffoldTemplate(ctx context.Context, gc *gitea.Client, giteaURL, templateID, appName, namespace string, overrides map[string]string) (*scaffoldedRepo, error) {
	t, err := loadBackstageTemplate(gc, templateID)
	if err != nil {
		return nil, err
	}
	skeleton, valueSpec, ok := t.fetchTemplateStep()
	if !ok {
		return nil, fmt.Errorf("template %q has no fetch:template step, so there is no skeleton to render", templateID)
	}

	host := "gitea"
	if u, uerr := url.Parse(giteaURL); uerr == nil && u.Host != "" {
		host = u.Host
	}
	params, err := resolveParameters(t, appName, globals.GiteaPlatformOrg, appName, host, overrides)
	if err != nil {
		return nil, err
	}
	values, err := buildValues(valueSpec, params)
	if err != nil {
		return nil, err
	}
	// `namespace` is not a declared parameter on every template, but every
	// skeleton's deploy/ needs one; the CLI's --namespace is the answer.
	if _, set := values["namespace"]; !set {
		values["namespace"] = namespace
	}
	// Pin the public hostname to THIS platform's domain.
	//
	// The shipped templates compute their hostname as
	// `<name>.adhar.localtest.me` — the local-development domain. On a real
	// install that produced an HTTPRoute for a hostname the cluster does not
	// serve, so the scaffolded service came with a URL that could never resolve.
	// A template cannot know the domain; the scaffolder can, so it decides. The
	// Console's scaffolder pins the same two values the same way, which is what
	// keeps a CLI-scaffolded repo and a Console-scaffolded repo identical.
	if base := platformBaseDomain(host); base != "" {
		values["platformBaseDomain"] = base
		values["hostname"] = appName + "." + base
	}
	ctxVals := map[string]any{"values": values, "parameters": params}

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

	repo, err := ensureScaffoldRepo(gc, appName, t.Metadata.Description)
	if err != nil {
		return nil, err
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	ops := make([]*gitea.ChangeFileOperation, 0, len(entries))
	for _, e := range entries {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		raw, _, gerr := gc.GetFile(globals.GiteaPlatformOrg, globals.GitOpsRepoTemplates, "main", e.Path)
		if gerr != nil {
			return nil, fmt.Errorf("reading %s: %w", e.Path, gerr)
		}
		// A file path may itself be templated (`${{ values.name }}.go`).
		rel, rerr := renderContent(strings.TrimPrefix(e.Path, prefix), ctxVals)
		if rerr != nil {
			return nil, fmt.Errorf("rendering the path of %s: %w", e.Path, rerr)
		}
		content := raw
		if isTextTemplate(rel) {
			rendered, rerr := renderContent(string(raw), ctxVals)
			if rerr != nil {
				return nil, fmt.Errorf("rendering %s: %w", e.Path, rerr)
			}
			content = []byte(rendered)
		}
		ops = append(ops, &gitea.ChangeFileOperation{
			Operation: "create",
			Path:      rel,
			Content:   base64.StdEncoding.EncodeToString(content),
		})
	}
	// ONE commit for the whole skeleton. Committing file by file made a
	// 14-file template 14 commits, and the repository's push webhook started
	// the app-ci pipeline 14 times in parallel — for a repository that was not
	// even complete until the last one. A scaffold is one change.
	if _, _, cerr := gc.ChangeFiles(globals.GiteaPlatformOrg, repo, gitea.ChangeFilesOptions{
		Files:   ops,
		Message: fmt.Sprintf("scaffold %s from %s", appName, templateID),
		Branch:  "main",
	}); cerr != nil {
		return nil, fmt.Errorf("committing %d files to %s/%s: %w", len(ops), globals.GiteaPlatformOrg, repo, cerr)
	}

	return &scaffoldedRepo{
		CloneURL:     fmt.Sprintf("%s/%s/%s.git", strings.TrimSuffix(giteaURL, "/"), globals.GiteaPlatformOrg, repo),
		ManifestPath: t.manifestPath(),
		Files:        len(entries),
	}, nil
}

// binaryExt are the extensions rendered verbatim: substituting into a PNG
// corrupts it, and no shipped skeleton templates one.
var binaryExt = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".ico": true,
	".webp": true, ".bmp": true, ".woff": true, ".woff2": true, ".ttf": true,
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
