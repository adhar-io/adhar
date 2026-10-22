package down

import (
	"errors"
	"testing"
)

// A teardown the UI reports as failed must not exit 0: scripts, `adhar down &&
// adhar up` and CI under a pseudo-terminal read the status, not the screen.
func TestAFailedInteractiveTeardownExitsNonZero(t *testing.T) {
	if code := teardownExitCode(downModel{err: errors.New("failed to delete cluster"), done: true}); code == 0 {
		t.Fatal("a failed teardown exited 0")
	}
	if code := teardownExitCode(downModel{quitting: true}); code == 0 {
		t.Fatal("an interrupted (unfinished) teardown exited 0")
	}
	if code := teardownExitCode(downModel{done: true}); code != 0 {
		t.Fatalf("a completed teardown exited %d", code)
	}
}
