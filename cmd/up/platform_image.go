package up

import (
	"os"
	"strings"

	"adhar-io/adhar/platform/controllers/adharplatform"
)

// seedPlatformImage tells the in-process manager which image catalogue steps
// run (`{{ .PlatformImage }}`, docs/design/0026 D-19, 0025 D10): the image this
// `adhar up` is about to install as the adhar-controlplane manager.
//
// It must be called BEFORE controllers.RunControllers. Only the CLI seeds the
// GitOps repos (the in-cluster manager has no stack directory), and on a fresh
// cluster it seeds before EnsureControllerManager has created the Deployment
// the reconciler would otherwise read the image from — so every fresh cluster
// shipped the adhar-batch-steps workflow step on ghcr.io/adhar-io/adhar:latest
// with imagePullPolicy IfNotPresent, a step whose CLI was not the release the
// control plane runs and that no later upgrade of the same tag would refresh.
//
// An ADHAR_PLATFORM_IMAGE the operator exported is theirs and is left alone.
func seedPlatformImage(image string) {
	image = strings.TrimSpace(image)
	if image == "" || strings.TrimSpace(os.Getenv(adharplatform.PlatformImageEnv)) != "" {
		return
	}
	_ = os.Setenv(adharplatform.PlatformImageEnv, image)
}
