package adharplatform

import "testing"

// The HTTP code curl prints is the LAST thing in the output, not the first.
//
// CombinedOutput merges kubectl's stderr, and a cluster with any unavailable
// aggregated API prints a discovery warning before every result. A HasPrefix test
// against that never matched, so a Gitea org that already existed — the normal case
// on every re-run — was reported as a failure and `adhar upgrade` could not push to
// an established cluster (2026-09-26).
func TestHTTPStatusFromOutput(t *testing.T) {
	noisy := "E0926 15:52:26.762631   99151 memcache.go:287] couldn't get resource list for " +
		"spdx.softwarecomposition.kubescape.io/v1beta1: the server is currently unable to handle the request\n" +
		"E0926 15:52:27.010449   99151 memcache.go:121] couldn't get resource list for x/v1: nope\n" +
		"422"

	for name, tc := range map[string]struct{ in, want string }{
		"bare code":            {"422", "422"},
		"code after stderr":    {noisy, "422"},
		"conflict":             {"some warning\n409", "409"},
		"success":              {"201", "201"},
		"trailing whitespace":  {"  404  ", "404"},
		"code glued to output": {"warning\nfailed422", "422"},
	} {
		if got := httpStatusFromOutput(tc.in); got != tc.want {
			t.Errorf("%s: httpStatusFromOutput = %q, want %q", name, got, tc.want)
		}
	}
}

// With no code to find, the output is returned so an error still says something.
func TestHTTPStatusFromOutputKeepsContextWhenThereIsNoCode(t *testing.T) {
	if got := httpStatusFromOutput("connection refused"); got != "connection refused" {
		t.Errorf("got %q, want the original text", got)
	}
	if got := httpStatusFromOutput("   "); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
