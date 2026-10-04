package provider

import (
	"os"
	"strings"
	"testing"
)

// A provider must not forget a cluster whose resources are still there.
//
// The state file is the only record of what was built, so dropping the entry
// while resources survive makes them unreachable: the next `adhar down` has
// nothing to work from, and the leftovers keep billing where nobody is looking.
//
// Live on 2026-10-04 (Azure): trackerFor read the tracker from the state file,
// so no Azure call could fail; the resource-group lookup then failed on a bad
// credential and was logged as a warning; execution fell through; the entry was
// deleted and saved; `adhar down` printed "Successfully deleted cluster" and
// exited 0. Left behind were 3 VMs, 3 OS disks, 3 NICs, 3 public IPs, a VNet and
// an NSG — 10 of 20 regional vCPUs — against a state file reading
// `{"clusters":{},"resourceTrackers":{}}`. They were recovered only by
// enumerating the subscription by hand.
//
// GCP had the same shape in a milder form: it returned the error correctly but
// still deleted the record first, so the leftovers were equally unfindable.
//
// Asserted on source order because the cloud SDK clients these functions call
// are concrete types with no interface to fake — there is no way to make
// resourceGroupClient.Get fail in a unit test. The invariant is positional and
// that is exactly what regressed.
func TestDeleteClusterKeepsTheRecordWhenResourcesSurvive(t *testing.T) {
	// Each provider names its collected failures differently.
	for _, tc := range []struct{ pkg, guard string }{
		{"azure", "if len(problems) > 0 {"},
		{"gcp", "if len(errors) > 0 {"},
	} {
		p := tc.pkg
		t.Run(p, func(t *testing.T) {
			body, err := deleteClusterBody(p + "/provider.go")
			if err != nil {
				t.Fatalf("reading %s: %v", p, err)
			}
			// The early return must be governed by the collected failures. Checking
			// only that the bail-out text sits above the `delete` calls is not
			// enough: neutering the condition to `if false` leaves the text exactly
			// where it was, and the first version of this test passed against that.
			if !strings.Contains(body, tc.guard) {
				t.Errorf("the failure check must be %q — a bail-out behind a condition that cannot "+
					"fire is dead code that reads like a safeguard", tc.guard)
			}
			bail := strings.Index(body, "was NOT fully deleted")
			if bail < 0 {
				t.Fatal("DeleteCluster must refuse to report success when anything is left behind, " +
					"and must say that the record was kept so the operation can be retried")
			}
			forget := strings.Index(body, "delete(p.clusters")
			if forget < 0 {
				t.Fatal("expected DeleteCluster to drop its tracking entry on the success path")
			}
			if forget < bail {
				t.Error("the cluster is forgotten BEFORE the failure check — a teardown that deleted " +
					"nothing would erase the only record of what exists. Move the `delete` calls after " +
					"the early return.")
			}
		})
	}
}

// Timing out while waiting for a managed cluster to disappear is not deletion.
//
// DigitalOcean and Civo both dropped the cluster and returned nil on timeout, so
// a cluster that was still running — and still billing — was reported as deleted
// and then forgotten.
func TestManagedDeleteDoesNotForgetOnTimeout(t *testing.T) {
	for _, p := range []string{"digitalocean", "civo"} {
		t.Run(p, func(t *testing.T) {
			body, err := deleteClusterBody(p + "/provider.go")
			if err != nil {
				t.Fatalf("reading %s: %v", p, err)
			}
			i := strings.Index(body, "timed out waiting for")
			if i < 0 {
				t.Skip("no deletion wait in this provider")
			}
			// Look at the timeout branch only: up to the end of that select case.
			branch := body[i:]
			if end := strings.Index(branch, "case <-ticker.C:"); end > 0 {
				branch = branch[:end]
			}
			if strings.Contains(branch, "delete(p.clusters") {
				t.Error("the timeout branch must not forget the cluster — it may still exist and " +
					"still be billing, and the record is what makes a retry possible")
			}
			if !strings.Contains(branch, "return fmt.Errorf") {
				t.Error("a timeout must be returned as an error, not as success")
			}
		})
	}
}

// deleteClusterBody returns the text of a provider's DeleteCluster.
func deleteClusterBody(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	src := string(b)
	start := strings.Index(src, "func (p *Provider) DeleteCluster(")
	if start < 0 {
		return "", os.ErrNotExist
	}
	rest := src[start:]
	// The function ends at the first line that is exactly a closing brace.
	if end := strings.Index(rest, "\n}\n"); end > 0 {
		rest = rest[:end]
	}
	return rest, nil
}

// saveState must replace the file by rename, not truncate it in place.
//
// Crash-safety cannot be simulated here without fault injection, so this pins
// the mechanism instead: os.WriteFile truncates and then writes, which is the
// window that turns a kill or a full disk into an empty state file and a cluster
// nobody can find. A temporary file plus rename(2) has no such window.
//
// This exists because the behavioural test above does NOT cover it — a
// round-trip assertion passes equally well against os.WriteFile, which is how
// the original bug would survive being "tested".
func TestAzureStateIsWrittenAtomically(t *testing.T) {
	b, err := os.ReadFile("azure/provider.go")
	if err != nil {
		t.Fatalf("reading azure provider: %v", err)
	}
	src := string(b)
	start := strings.Index(src, "func (p *Provider) saveState(")
	if start < 0 {
		t.Fatal("saveState has moved; this guard needs updating")
	}
	body := src[start:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}
	for _, want := range []string{"os.CreateTemp", "os.Rename"} {
		if !strings.Contains(body, want) {
			t.Errorf("saveState must write through %s so a partial write cannot destroy the "+
				"only record of what exists in the cloud", want)
		}
	}
	// Compare against CODE only. The function's own comment explains why
	// os.WriteFile is unsuitable, and the first version of this check matched
	// that explanation and failed on the correct implementation.
	if strings.Contains(stripComments(body), "os.WriteFile") {
		t.Error("saveState must not use os.WriteFile: it truncates before writing, so an " +
			"interrupted save leaves an empty or half-written state file")
	}
}

// stripComments removes // line comments so prose about an API is not mistaken
// for a call to it.
func stripComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
