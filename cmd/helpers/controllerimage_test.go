package helpers

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The in-cluster manager is what reconciles the platform after `adhar up`
// exits. Handing it an image that does not exist does not fail the bring-up —
// it leaves the cluster with nothing driving it, and the symptom appears a long
// way from the cause.
//
// Live, on AWS (2026-10-08): the CLI was built from tag `v0.1.34`, whose
// container image had never been published, so the manager sat in
// ImagePullBackOff for 47 minutes. The node autoscaler therefore never cleared
// `node.adhar.io/csi-not-ready` from a healthy autoscaled worker, a quarter of
// the cluster's CPU was fenced off, and `adhar-console` went red with
// `0/4 nodes are available: 2 Insufficient cpu, 2 node(s) had untolerated
// taint(s)`. Nothing in that chain mentions an image tag.

// The two images these tests distinguish. `unpublished` is the real tag that
// caused the incident: v0.1.34 exists in git and its image never reached ghcr.
const (
	unpublished = "ghcr.io/adhar-io/adhar:0.1.34"
	published   = "ghcr.io/adhar-io/adhar:0.1.33"
)

func TestDerivedControllerImageFallsBackWhenItWasNeverPublished(t *testing.T) {
	missing := func(context.Context, string) bool { return false }

	got := ResolveControllerImage(context.Background(), "", unpublished, missing, nil)
	if got != FallbackControllerImage {
		t.Errorf("resolveControllerImage = %q, want the %q fallback: a version-derived tag with no published image "+
			"leaves the platform with no controller at all", got, FallbackControllerImage)
	}
}

func TestDerivedControllerImageIsKeptWhenItExists(t *testing.T) {
	present := func(context.Context, string) bool { return true }

	got := ResolveControllerImage(context.Background(), "", published, present, nil)
	if got != published {
		t.Errorf("resolveControllerImage = %q, want the release's own image %q: a published version-derived "+
			"image must not be replaced by :latest", got, published)
	}
}

// An explicit choice — `--controller-image`, or the image already deployed — is
// the operator's, and it is never silently swapped for something else while it
// works. When it does NOT work there is a judgement to make, and these three
// cases are it.
func TestAnExplicitControllerImageIsCheckedNotAssumed(t *testing.T) {
	t.Run("kept when it exists, and nothing else is consulted", func(t *testing.T) {
		var probed []string
		probe := func(_ context.Context, ref string) bool { probed = append(probed, ref); return true }

		got := ResolveControllerImage(context.Background(), published, unpublished, probe, nil)
		if got != published {
			t.Errorf("resolved %q, want the operator's own %q", got, published)
		}
		if len(probed) != 1 || probed[0] != published {
			t.Errorf("probed %v, want exactly the configured image: the check is what produces the warning", probed)
		}
	})

	t.Run("kept when nothing better exists, with a warning", func(t *testing.T) {
		var warned []string
		missing := func(context.Context, string) bool { return false }

		got := ResolveControllerImage(context.Background(), unpublished, "", missing,
			func(m string) { warned = append(warned, m) })
		if got != unpublished {
			t.Errorf("resolved %q, want the operator's own %q", got, unpublished)
		}
		if len(warned) == 0 {
			t.Error("no warning: a manager that cannot start is the one thing the operator must be told about")
		} else if !strings.Contains(warned[0], unpublished) {
			t.Errorf("the warning does not name the image: %q", warned[0])
		}
	})

	// This is what lets `adhar upgrade` repair a cluster that is already stuck on
	// an unpullable image. Preserving the deployed value unconditionally means an
	// upgrade run to fix the problem keeps it.
	t.Run("replaced when the deployed image cannot be pulled and this build can", func(t *testing.T) {
		var warned []string
		probe := func(_ context.Context, ref string) bool { return ref == published }

		got := ResolveControllerImage(context.Background(), unpublished, published, probe,
			func(m string) { warned = append(warned, m) })
		if got != published {
			t.Errorf("resolved %q, want %q: a deployed image that cannot be pulled is not worth keeping", got, published)
		}
		if len(warned) == 0 {
			t.Error("the substitution was silent")
		}
	})
}

// A probe that cannot answer must never downgrade the image: an air-gapped
// install, a private registry or a 5xx are all "unknown", not "missing".
func TestAnUnreachableRegistryDoesNotChangeTheImage(t *testing.T) {
	unknown := func(context.Context, string) bool { return true }
	if got := ResolveControllerImage(context.Background(), "", unpublished, unknown, nil); got != unpublished {
		t.Errorf("an unanswerable probe changed the image to %q; unknown must count as present", got)
	}
}

// splitImageRef decides what the probe can even ask about. Getting this wrong in
// the permissive direction is safe (treated as present); getting it wrong the
// other way would replace a perfectly good image.
func TestOnlyGhcrTagsAreProbed(t *testing.T) {
	for _, tc := range []struct {
		ref      string
		repo     string
		tag      string
		probable bool
	}{
		{"ghcr.io/adhar-io/adhar:0.1.34", "adhar-io/adhar", "0.1.34", true},
		{"ghcr.io/adhar-io/adhar:latest", "adhar-io/adhar", "latest", true},
		// A digest is its own proof; nothing to look up.
		{"ghcr.io/adhar-io/adhar@sha256:abc", "", "", false},
		// Another registry: the token flow below does not apply.
		{"docker.io/library/nginx:1.27", "", "", false},
		{"ghcr.io/adhar-io/adhar", "", "", false},
		{"", "", "", false},
	} {
		repo, tag, ok := SplitGhcrRef(tc.ref)
		if ok != tc.probable {
			t.Errorf("SplitGhcrRef(%q) probable = %v, want %v", tc.ref, ok, tc.probable)
			continue
		}
		if ok && (repo != tc.repo || tag != tc.tag) {
			t.Errorf("SplitGhcrRef(%q) = (%q, %q), want (%q, %q)", tc.ref, repo, tag, tc.repo, tc.tag)
		}
	}
}

// The default itself: a development build has no published image and must not
// try to run one.
func TestDevelopmentBuildsTrackLatest(t *testing.T) {
	got := DefaultControllerImage("v0.1.34")
	if !strings.HasPrefix(got, "ghcr.io/adhar-io/adhar:") {
		t.Errorf("defaultControllerImage = %q, want a ghcr.io/adhar-io/adhar reference", got)
	}
}

// The probe's decision, separated from its transport. Only a definite 404 may
// change the image; an air-gapped install (transport error), a private registry
// (401) and a bad day at the registry (5xx) must all leave it alone.
func TestOnlyA404MeansTheImageIsMissing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		err    error
		want   bool
	}{
		{"published", 200, nil, true},
		{"never published", 404, nil, false},
		{"needs credentials", 401, nil, true},
		{"registry having a bad day", 503, nil, true},
		{"no network at all", 0, errUnreachable, true},
	} {
		if got := ImagePresenceFromStatus(tc.status, tc.err); got != tc.want {
			t.Errorf("%s: ImagePresenceFromStatus(%d, %v) = %v, want %v",
				tc.name, tc.status, tc.err, got, tc.want)
		}
	}
}

var errUnreachable = errors.New("dial tcp: lookup ghcr.io: no such host")
