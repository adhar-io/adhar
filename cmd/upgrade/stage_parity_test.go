package upgrade

import (
	"strings"
	"testing"
)

// The diff and the push must stage the stack the same way.
//
// The push is reconciler.ApplyPlatformStack, which renders `{{ .PlatformImage }}`
// (the argo-workflows step catalogue, design 0025 §12.1) as the image the
// manager Deployment runs. adharplatform.StageStack, the function the diff
// used, has no cluster and renders a fallback, so the batch step's file was
// reported as changed on every upgrade, a no-op included. The diff now stages
// through the same reconciler (TestBatchStepUpgradeDiffStagesTheImageThePushWrites
// in platform/controllers/adharplatform holds the method to that).
func TestUpgradeDiffStagesTheStackLikeThePush(t *testing.T) {
	src := readPackageSource(t)
	if strings.Contains(src, "adharplatform.StageStack(") {
		t.Error("cmd/upgrade stages the stack with adharplatform.StageStack, which cannot know the manager's image; " +
			"use reconciler.StageStackAsSeeded so the diff matches what ApplyPlatformStack pushes")
	}
	if !strings.Contains(src, "reconciler.StageStackAsSeeded(") {
		t.Error("cmd/upgrade's diff must stage the stack through reconciler.StageStackAsSeeded")
	}
}
