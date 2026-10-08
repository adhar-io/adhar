package helpers

// Which image the in-cluster controller manager runs from — and the check that
// it exists.
//
// WHY THIS IS SHARED. `adhar up` chooses the image; `adhar upgrade` keeps
// whatever is deployed. Both derived the fallback tag from the CLI's own
// version, which assumes every released version has a published image. `v0.1.34`
// was tagged in git and its container image never reached ghcr, so a freshly
// built CLI installed `ghcr.io/adhar-io/adhar:0.1.34`, the manager sat in
// ImagePullBackOff, and NOTHING reconciled the platform once `adhar up` exited.
//
// The symptom surfaced a long way from the cause (live AWS cluster,
// 2026-10-08): with no autoscaler running, `node.adhar.io/csi-not-ready` was
// never lifted from a healthy autoscaled worker, a quarter of the cluster's CPU
// was fenced off, and `adhar-console` went red with `0/4 nodes are available:
// 2 Insufficient cpu, 2 node(s) had untolerated taint(s)`. Nothing in that chain
// mentions an image tag — which is why the check belongs at the moment the image
// is chosen, not in the error that eventually comes out.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// FallbackControllerImage is what a build with no published image runs — the
// same image the development path already uses.
const FallbackControllerImage = "ghcr.io/adhar-io/adhar:latest"

// controllerImageRepo is the published repository for the manager.
const controllerImageRepo = "ghcr.io/adhar-io/adhar:"

// DefaultControllerImage is the released image matching a CLI version
// (goreleaser tags images without the leading "v"). A development build
// ("v0.0.1-dev", or any pre-release suffix) has no published image and tracks
// latest instead.
func DefaultControllerImage(version string) string {
	v := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if v == "" || strings.Contains(v, "-dev") || strings.HasPrefix(v, "0.0.1") {
		return FallbackControllerImage
	}
	return controllerImageRepo + v
}

// ResolveControllerImage decides which image to install, and refuses to hand the
// cluster one that does not exist.
//
// `configured` is an explicit operator choice (`--controller-image`, or the
// image already deployed). It is never silently replaced — but it IS checked,
// because the warning is the only hint anyone gets before the cluster goes
// quiet. `derived` is this build's own default, which may legitimately be
// swapped for the fallback.
//
// `warn` receives anything the operator needs to know; nil is allowed.
func ResolveControllerImage(
	ctx context.Context,
	configured, derived string,
	exists func(context.Context, string) bool,
	warn func(string),
) string {
	say := func(msg string) {
		if warn != nil {
			warn(msg)
		}
	}

	if configured != "" {
		if exists(ctx, configured) {
			return configured
		}
		// A deployed image that cannot be pulled is not a choice worth keeping:
		// preserving it is how a cluster stays broken across an upgrade that was
		// run to fix it. An explicit flag still wins, so the caller decides by
		// what it passes as `derived`.
		if derived != "" && derived != configured && exists(ctx, derived) {
			say(fmt.Sprintf("The controller image %s is not in its registry; using %s instead", configured, derived))
			return derived
		}
		say(fmt.Sprintf("The controller image %s could not be found in its registry; the in-cluster manager will "+
			"not start, and nothing will reconcile this platform", configured))
		return configured
	}

	if derived == "" || exists(ctx, derived) {
		return derived
	}
	say(fmt.Sprintf("No published image for this build (%s); the in-cluster manager will run %s instead. "+
		"Pass --controller-image to pin a different one", derived, FallbackControllerImage))
	return FallbackControllerImage
}

// ControllerImageExists reports whether a registry actually serves this image.
//
// UNKNOWN COUNTS AS YES. A probe that cannot reach the registry — an air-gapped
// install, a private registry needing credentials, a 5xx — must not downgrade
// the image the operator is entitled to run. Only a definite 404 is "not there".
func ControllerImageExists(ctx context.Context, ref string) bool {
	repo, tag, ok := SplitGhcrRef(ref)
	if !ok {
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Anonymous pull token: even a public image needs one to HEAD a manifest.
	tokenURL := fmt.Sprintf("https://ghcr.io/token?scope=repository:%s:pull&service=ghcr.io", repo)
	treq, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
	if err != nil {
		return true
	}
	tresp, err := http.DefaultClient.Do(treq)
	if err != nil {
		return true
	}
	defer tresp.Body.Close()
	var token struct{ Token string }
	if err := json.NewDecoder(tresp.Body).Decode(&token); err != nil || token.Token == "" {
		return true
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodHead,
		fmt.Sprintf("https://ghcr.io/v2/%s/manifests/%s", repo, tag), nil)
	if err != nil {
		return true
	}
	req.Header.Set("Authorization", "Bearer "+token.Token)
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ","))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ImagePresenceFromStatus(0, err)
	}
	defer resp.Body.Close()
	return ImagePresenceFromStatus(resp.StatusCode, nil)
}

// ImagePresenceFromStatus is the probe's actual decision, separated from the
// transport so it can be tested: only a definite 404 means "not there", and
// every other outcome — a transport error, a 401, a 5xx — means "do not touch
// the operator's image".
func ImagePresenceFromStatus(status int, err error) bool {
	if err != nil {
		return true
	}
	return status != http.StatusNotFound
}

// SplitGhcrRef pulls the repository and tag out of a ghcr.io reference.
//
// Only ghcr.io, and only a tag: that is what this platform publishes and what
// the token flow above speaks. Anything else returns false and is treated as
// present, which is the safe answer for an image we cannot ask about.
func SplitGhcrRef(ref string) (repo, tag string, ok bool) {
	const host = "ghcr.io/"
	if !strings.HasPrefix(ref, host) {
		return "", "", false
	}
	rest := strings.TrimPrefix(ref, host)
	if strings.Contains(rest, "@") {
		// A digest reference is its own proof of existence.
		return "", "", false
	}
	i := strings.LastIndex(rest, ":")
	if i <= 0 || i == len(rest)-1 {
		return "", "", false
	}
	return rest[:i], rest[i+1:], true
}
