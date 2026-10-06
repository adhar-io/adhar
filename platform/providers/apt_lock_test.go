package provider

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every apt-get this package runs on a node must WAIT for the dpkg lock.
//
// Ubuntu starts unattended-upgrades on first boot and it holds
// /var/lib/dpkg/lock-frontend for the first minute or two — exactly the window
// cloud-init runs node prep in. Without -o DPkg::Lock::Timeout, apt does not
// queue, it exits immediately:
//
//	E: Could not get lock /var/lib/dpkg/lock-frontend.
//	   It is held by process 1806 (unattended-upgr)
//
// cloud-init marks scripts_user failed, kubeadm is never installed, and the node
// joins nothing. What the operator sees is the create sitting in WaitForNodePrep
// for its full 15 minutes and then failing with a timeout that never mentions
// apt. Because it is a race it breaks only some bring-ups: one live Civo run
// joined all three nodes and the next lost both workers to it (2026-10-06).
//
// It is shared code, so this is every kubeadm provider: AWS, Azure, GCP,
// DigitalOcean, Civo and custom.
func TestEveryAptGetWaitsForTheDpkgLock(t *testing.T) {
	// The rendered node-prep script: each apt-get must carry the wait.
	script := KubeadmNodePrepScript("1.37")
	// The ASSIGNMENT, with a real timeout — not merely the words appearing
	// somewhere. Checking `strings.Contains(script, "DPkg::Lock::Timeout")`
	// passed happily when the constant was emptied, because the comment above
	// the assignment names the option too.
	assign := regexp.MustCompile(`APT_WAIT="-o DPkg::Lock::Timeout=([1-9][0-9]*)"`)
	if m := assign.FindStringSubmatch(script); m == nil {
		t.Fatal("the node-prep script does not set APT_WAIT to a non-zero DPkg::Lock::Timeout; " +
			"unattended-upgrades will break some bring-ups")
	}
	if !regexp.MustCompile(`aptLockWait = "-o DPkg::Lock::Timeout=[1-9][0-9]*"`).MatchString(readSource(t, "kubeadm.go")) {
		t.Error("aptLockWait is not a non-zero DPkg::Lock::Timeout, so every apt call that uses it waits for nothing")
	}
	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "apt-get ") {
			continue
		}
		if !strings.Contains(trimmed, "$APT_WAIT") {
			t.Errorf("node-prep runs %q without $APT_WAIT, so it fails instead of waiting for the dpkg lock", trimmed)
		}
	}

	// The upgrade path runs apt over SSH against nodes that may be running
	// unattended-upgrades too, so its commands need the same treatment. Checked
	// in the source because the steps are built inside the function.
	for _, line := range strings.Split(readSource(t, "kubeadm.go"), "\n") {
		trimmed := strings.TrimSpace(line)
		// Skip comments and the script's own lines (covered above).
		if strings.HasPrefix(trimmed, "//") || strings.Contains(trimmed, "$APT_WAIT") || strings.Contains(trimmed, `APT_WAIT="`) {
			continue
		}
		if !regexp.MustCompile(`apt-get (update|install|upgrade)`).MatchString(trimmed) {
			continue
		}
		if !strings.Contains(trimmed, "aptLockWait") {
			t.Errorf("kubeadm.go runs apt without the lock wait: %s", trimmed)
		}
	}
}

// readSource returns a file from this package, for the checks that have to look
// at code rather than at rendered output.
func readSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
