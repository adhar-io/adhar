package provider

import (
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Every pinned upstream manifest URL must still exist.
//
// This is not hypothetical. The first live Civo bring-up failed AFTER creating
// three billable instances because both of its pinned refs had rotted: the
// cloud-controller-manager was pinned to v0.0.24 when upstream's tags are v0.1.x
// AND its manifest had moved path, and the CSI driver was pinned to v0.2.0 when
// upstream is on v0.10.x. Nothing caught it because a URL constant compiles
// perfectly and is only resolved over SSH on a real node, a quarter of an hour
// into a paid provisioning run.
//
// NETWORK TEST, so it is opt-in: set ADHAR_NET_TESTS=1. CI that cannot reach
// GitHub should not fail, but someone bumping a pin should be able to check it in
// one command:
//
//	ADHAR_NET_TESTS=1 go test ./platform/providers/ -run TestPinnedUpstreamRefs
func TestPinnedUpstreamRefsStillResolve(t *testing.T) {
	if os.Getenv("ADHAR_NET_TESTS") != "1" {
		t.Skip("network test; set ADHAR_NET_TESTS=1 to check the pinned upstream refs")
	}

	// Collected from the provider sources rather than listed here, so a new pin is
	// covered the moment it is added instead of when somebody remembers.
	urls := map[string][]string{}
	for _, dir := range []string{"civo", "digitalocean", "aws", "azure", "gcp"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			raw, err := os.ReadFile(dir + "/" + e.Name())
			if err != nil {
				t.Fatalf("reading %s/%s: %v", dir, e.Name(), err)
			}
			for _, u := range extractPinnedURLs(string(raw)) {
				urls[u] = append(urls[u], dir+"/"+e.Name())
			}
		}
	}
	if len(urls) == 0 {
		t.Skip("no pinned raw-manifest URLs found in the provider sources")
	}

	client := &http.Client{Timeout: 20 * time.Second}
	for u, where := range urls {
		t.Run(u, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodHead, u, nil)
			if err != nil {
				t.Fatalf("building the request: %v", err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Skipf("could not reach %s (%v) — treating an unreachable network as no result", u, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("%s returned %d — the pin in %s has rotted. A create will fail on this AFTER the instances exist and bill.",
					u, resp.StatusCode, strings.Join(where, ", "))
			}
		})
	}
}

// extractPinnedURLs finds raw-manifest URLs and kustomize refs in Go source, and
// turns the latter into something fetchable.
func extractPinnedURLs(src string) []string {
	var out []string
	// Only URLs naming a FILE. Several constants are BASES that get a filename
	// appended at the call site (doCSIReleaseBase + "/driver.yaml") or are Helm
	// repository roots for `helm repo add`, and a HEAD on a directory always 404s —
	// checking those reported three healthy pins as rotted.
	raw := regexp.MustCompile(`https://raw\.githubusercontent\.com/[^\s"]+\.ya?ml`)
	out = append(out, raw.FindAllString(src, -1)...)

	// `github.com/<owner>/<repo>/<path>?ref=<tag>` is what kustomize takes; its
	// kustomization.yaml is the file that has to exist.
	kust := regexp.MustCompile(`github\.com/([\w.-]+)/([\w.-]+)/([\w./-]+)\?ref=([\w.-]+)`)
	for _, m := range kust.FindAllStringSubmatch(src, -1) {
		out = append(out, "https://raw.githubusercontent.com/"+m[1]+"/"+m[2]+"/"+m[4]+"/"+m[3]+"/kustomization.yaml")
	}
	return out
}
