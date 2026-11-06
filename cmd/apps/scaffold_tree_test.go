package apps

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"adhar-io/adhar/globals"

	"code.gitea.io/sdk/gitea"
)

// pagedTemplatesGitea serves a templates repository whose tree is longer than
// one page, the way Gitea 1.27 pages a recursive listing: at most perPage
// entries a page whatever the client asks (the SDK's `limit` is not read),
// total_count on every page, and `truncated` on every page but the last.
type pagedTemplatesGitea struct {
	perPage int
	// everyPageTruncated: `truncated` on every page while the tree is longer
	// than one, the last included, as older Gitea releases answered; the
	// empty page past the end carries no flag.
	everyPageTruncated bool
	// truncatedPastTheEnd: the empty page past the end says `truncated` too.
	truncatedPastTheEnd bool
	// ignorePage: page 1 whatever page is asked for (a proxy dropping it).
	ignorePage bool
	// claimed overrides total_count; -1 leaves it out.
	claimed int

	mu   sync.Mutex
	refs []string // the ref each tree page was asked for, in order
}

func (p *pagedTemplatesGitea) serve(t *testing.T, files map[string]string) *gitea.Client {
	t.Helper()
	paths := make([]string, 0, len(files))
	for f := range files {
		paths = append(paths, f)
	}
	sort.Strings(paths)
	treeBase := "/api/v1/repos/" + globals.GiteaPlatformOrg + "/" + globals.GitOpsRepoTemplates + "/git/trees/"
	rawBase := "/api/v1/repos/" + globals.GiteaPlatformOrg + "/" + globals.GitOpsRepoTemplates + "/raw/"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, treeBase):
			ref := strings.TrimPrefix(r.URL.Path, treeBase)
			p.mu.Lock()
			p.refs = append(p.refs, ref)
			p.mu.Unlock()
			if ref != "main" && ref != "tree-sha-1" {
				http.NotFound(w, r)
				return
			}
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			if page < 1 || p.ignorePage {
				page = 1
			}
			resp := map[string]any{"sha": "tree-sha-1", "page": page, "tree": []map[string]string{}}
			switch {
			case p.claimed > 0:
				resp["total_count"] = p.claimed
			case p.claimed == 0:
				resp["total_count"] = len(paths)
			}
			start := (page - 1) * p.perPage
			if start < len(paths) {
				end := min(start+p.perPage, len(paths))
				var entries []map[string]string
				for _, f := range paths[start:end] {
					entries = append(entries, map[string]string{"path": f, "type": "blob"})
				}
				resp["tree"] = entries
				resp["truncated"] = end < len(paths) || (p.everyPageTruncated && len(paths) > p.perPage)
			} else if p.truncatedPastTheEnd {
				resp["truncated"] = true
			}
			_ = json.NewEncoder(w).Encode(resp)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, rawBase):
			content, ok := files[strings.TrimPrefix(r.URL.Path, rawBase)]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(content))
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	gc, err := gitea.NewClient(srv.URL, gitea.SetGiteaVersion("1.27.0"))
	if err != nil {
		t.Fatal(err)
	}
	return gc
}

// A templates repository whose tree holds another template first and then the
// one being scaffolded, its skeleton falling on both sides of the page break.
func treeAcrossPages(perPage int) (map[string]string, []string) {
	files := map[string]string{}
	for i := 0; i < perPage-3; i++ {
		files[fmt.Sprintf("templates/aaa-other/skeleton/f%04d.txt", i)] = "x"
	}
	prefix := globals.GitOpsTemplatesPath + "/go-web-service/skeleton/"
	skeleton := []string{"README.md", "catalog-info.yaml", "deploy/base/kustomization.yaml", "deploy/envs/dev/kustomization.yaml",
		"deploy/envs/prod/kustomization.yaml", "deploy/envs/test/kustomization.yaml", "main.go"}
	for _, f := range skeleton {
		files[prefix+f] = "# ${{ values.name }}\n"
	}
	files["tests/README.md"] = "not a template"
	return files, skeleton
}

// Gitea pages a recursive tree listing (1000 entries a page by default), and
// the templates repository outgrew one page on 2026-11-05 (1094 entries).
// The scaffold read page 1 and refused it as truncated, so EVERY template
// failed to scaffold; read to the end, the template renders whole.
func TestATemplatesTreeLongerThanOnePageIsReadToTheEnd(t *testing.T) {
	for _, mode := range []struct {
		name string
		g    *pagedTemplatesGitea
	}{
		{"gitea 1.27", &pagedTemplatesGitea{perPage: 5}},
		{"every page truncated", &pagedTemplatesGitea{perPage: 5, everyPageTruncated: true}},
		{"truncated past the end", &pagedTemplatesGitea{perPage: 5, everyPageTruncated: true, truncatedPastTheEnd: true}},
	} {
		files, skeleton := treeAcrossPages(5)
		gc := mode.g.serve(t, files)
		r, err := renderScaffold(context.Background(), gc, testTemplate(t), "https://gitea.example.io:443", "go-web-service", "billing", "dev", nil)
		if err != nil {
			t.Fatalf("%s: %v", mode.name, err)
		}
		got := append([]string(nil), r.paths...)
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(skeleton, ",") {
			t.Errorf("%s: rendered %v, want the whole skeleton %v", mode.name, got, skeleton)
		}
		// Page 1 resolves main; every later page asks for THAT tree, so a
		// push between two pages cannot mix two commits.
		refs := mode.g.refs
		if len(refs) < 2 || refs[0] != "main" {
			t.Fatalf("%s: tree pages asked for %v", mode.name, refs)
		}
		for _, ref := range refs[1:] {
			if ref != "tree-sha-1" {
				t.Errorf("%s: a later page asked for %q, not the tree page 1 resolved (%v)", mode.name, ref, refs)
			}
		}
	}
}

// A listing that ends before the total it announced is refused rather than
// rendered as a template with fewer files.
func TestATemplatesTreeThatEndsShortIsRefused(t *testing.T) {
	files, _ := treeAcrossPages(5)
	g := &pagedTemplatesGitea{perPage: 5, claimed: len(files) + 7}
	gc := g.serve(t, files)
	_, err := renderScaffold(context.Background(), gc, testTemplate(t), "https://gitea.example.io:443", "go-web-service", "billing", "dev", nil)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("lists %d entries but only %d were returned", len(files)+7, len(files))) {
		t.Fatalf("render error = %v, want the short listing refused", err)
	}
}

// A server or proxy that drops `page` answers page 1 every time: the listing
// would repeat itself (and the scaffold commit doubled files) or never end.
func TestATemplatesTreeListingThatRepeatsItselfIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name    string
		claimed int
		want    string
	}{
		{"with a total", 0, "returned more than the"},
		{"without a total", -1, fmt.Sprintf("still truncated after %d pages", templatesTreeMaxPages)},
	} {
		files, _ := treeAcrossPages(5)
		g := &pagedTemplatesGitea{perPage: 5, ignorePage: true, claimed: tc.claimed}
		gc := g.serve(t, files)
		_, err := renderScaffold(context.Background(), gc, testTemplate(t), "https://gitea.example.io:443", "go-web-service", "billing", "dev", nil)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: render error = %v, want %q", tc.name, err, tc.want)
		}
	}
}
