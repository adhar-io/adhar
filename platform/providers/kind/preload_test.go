package kind

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPreloadIsTheCiliumCriticalPathOnly(t *testing.T) {
	imgs := preloadImages()
	if len(imgs) != 3 {
		t.Fatalf("the save/load preload must stay small (the 7 GB core set cost three minutes per run); got %d images", len(imgs))
	}
	for _, img := range imgs {
		if !strings.HasPrefix(img, "quay.io/cilium/") {
			t.Errorf("only Cilium data-path images belong on the save/load path: %s", img)
		}
		if !strings.Contains(img, ":") {
			t.Errorf("preload images must be tagged: %s", img)
		}
	}
	imgs[0] = "mutated"
	if preloadImages()[0] == "mutated" {
		t.Error("preloadImages must return a copy")
	}
}

func TestImagesToPreloadSkipsCachedAndAbsentImages(t *testing.T) {
	cached := "quay.io/cilium/cilium:v1.20.0"
	f := &fakeEngine{answers: map[string]struct {
		out string
		err error
	}{
		"exec adhar-registry-cache-quay-io test -f /var/lib/registry/quay.io/docker/registry/v2/repositories/cilium/cilium/": answer("", nil),
		"exec": answer("", errors.New("exit 1")),
	}}
	hostHas := func(img string) bool { return !strings.Contains(img, "operator-generic") }
	got := imagesToPreload(context.Background(), f.run, preloadImages(), hostHas)
	if len(got) != 1 || !strings.Contains(got[0], "cilium-envoy") {
		t.Errorf("expected only the envoy image (cilium is cached, operator absent from host); got %v", got)
	}
	_ = cached
}
