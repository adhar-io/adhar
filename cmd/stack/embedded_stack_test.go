package stack

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	platformstack "adhar-io/adhar/platform/stack"
)

// The binary's own stack is extracted to a cache that is rewritten whenever it
// differs from what was built in. A toggle that edited it would report success
// and be undone before `adhar upgrade` could push it, so it must refuse and say
// how to get a copy that can be edited.
func TestTogglingTheEmbeddedStackIsRefusedWithAWayForward(t *testing.T) {
	oldDir, oldProfile, oldSource := flagStackDir, flagProfile, stackSource
	t.Cleanup(func() { flagStackDir, flagProfile, stackSource = oldDir, oldProfile, oldSource })
	flagProfile = "production"
	stackSource = platformstack.Source{Dir: t.TempDir(), Origin: platformstack.OriginEmbedded}
	flagStackDir = stackSource.Dir

	err := runToggle(&cobra.Command{}, []string{"harbor"}, true)
	if err == nil {
		t.Fatal("enable edited the read-only stack built into the binary")
	}
	for _, want := range []string{"adhar stack export", platformstack.EnvStackDir, "Marketplace"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q:\n%v", want, err)
		}
	}
}
