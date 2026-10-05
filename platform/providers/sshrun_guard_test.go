package provider

import (
	"strings"
	"testing"
	"time"
)

// SSHRun must fail cleanly, not panic, when it has nothing to authenticate with.
//
// Without the guard `ssh.PublicKeys(nil)` panics inside the handshake — but
// only once a handshake happens. On a developer laptop the dial to an empty
// address is refused first and the function returns an error, so
// TestScaleKubernetesVersionRefusesToGuess passed locally; on a GitHub runner
// sshd answers on localhost, the handshake ran, and the Tests workflow died
// with a nil-pointer dereference (2026-10-04). The assertion on the message is
// what makes this guard load-bearing on both kinds of machine.
func TestSSHRunRefusesWithoutASigner(t *testing.T) {
	_, err := SSHRun(nil, "adhar", "", "true", time.Second)
	if err == nil {
		t.Fatal("expected an error with no signer and no address")
	}
	if !strings.Contains(err.Error(), "no SSH signer") {
		t.Errorf("the error must name the missing credential, got: %v", err)
	}
	_, err = ControlPlaneVersion(nil, "adhar", "")
	if err == nil || !strings.Contains(err.Error(), "no SSH signer") {
		t.Errorf("ControlPlaneVersion must surface the same refusal, got: %v", err)
	}
}
