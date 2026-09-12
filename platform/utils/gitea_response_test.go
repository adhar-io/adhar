package utils

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gitea "code.gitea.io/sdk/gitea"
)

// The Gitea SDK returns a NIL *Response together with a non-nil error whenever
// the request never reached the server. That is the expected path during
// `adhar up`, which polls Gitea before it is serving, and reading resp.Status
// there panicked the reconciler with a nil dereference instead of reporting the
// dial error.
func TestResponseStatusHandlesNilResponse(t *testing.T) {
	if got := responseStatus(nil); got != "" {
		t.Fatalf("a nil response must render as empty, got %q", got)
	}

	// A Response struct whose embedded *http.Response is nil is the same hazard.
	if got := responseStatus(&gitea.Response{}); got != "" {
		t.Fatalf("a response with no http.Response must render as empty, got %q", got)
	}
}

func TestResponseStatusRendersRealStatus(t *testing.T) {
	resp := &gitea.Response{Response: &http.Response{Status: "403 Forbidden"}}
	got := responseStatus(resp)
	if !strings.Contains(got, "403 Forbidden") {
		t.Fatalf("a real status must appear in the message, got %q", got)
	}
}

// End to end over the real SDK: pointing the client at a closed port must
// return an error, not panic. Before the fix this test paniced.
func TestGetGiteaTokenOnUnreachableServerReturnsError(t *testing.T) {
	// Start and immediately close a server so the port is certainly refusing.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("GetGiteaToken panicked on an unreachable server: %v", r)
		}
	}()

	_, err := GetGiteaToken(context.Background(), url, "gitea_admin", "whatever")
	if err == nil {
		t.Fatal("expected an error from an unreachable gitea, got nil")
	}
}
