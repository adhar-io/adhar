package provider

import (
	"errors"
	"strings"
	"testing"
)

// The two AWS access failures are worded almost identically and need completely
// different people to fix. Telling them apart is the whole point: on a real
// account an operator read "not authorized", attached AdministratorAccess, and
// nothing changed — because an Organizations SCP is a ceiling above IAM.
func TestExplainAccessErrorSeparatesSCPFromMissingGrant(t *testing.T) {
	scp := errors.New("api error UnauthorizedOperation: You are not authorized to perform this " +
		"operation. User: arn:aws:iam::1:user/adhar is not authorized to perform: ec2:DescribeVpcs " +
		"with an explicit deny in a service control policy: arn:aws:organizations::2:policy/o-x/p-y")
	got := ExplainAccessError(scp)
	if !strings.Contains(got, "MANAGEMENT ACCOUNT") {
		t.Errorf("an SCP denial must point at the management account, got: %s", got)
	}
	if !strings.Contains(got, "AdministratorAccess will not help") {
		t.Errorf("an SCP denial must say more IAM will not help, got: %s", got)
	}

	iam := errors.New("User: arn:aws:iam::1:user/adhar is not authorized to perform: " +
		"ec2:DescribeVpcs because no identity-based policy allows the ec2:DescribeVpcs action")
	got = ExplainAccessError(iam)
	if strings.Contains(got, "MANAGEMENT ACCOUNT") {
		t.Errorf("a missing grant must NOT be blamed on an SCP, got: %s", got)
	}
	if !strings.Contains(got, "THIS account") {
		t.Errorf("a missing grant must be fixable locally, got: %s", got)
	}

	if ExplainAccessError(nil) != "" {
		t.Error("no error means no advice")
	}
}

// A quota must be judged against the cluster at FULL size. Sizing to the starting
// node count is how a cluster boots fine and then cannot grow.
func TestQuotaCheckJudgesAgainstFullSize(t *testing.T) {
	if c := QuotaCheck("vCPU", 64, 56, "vCPU", "raise it"); c.Status != CheckPass {
		t.Errorf("64 available for 56 needed should pass, got %s: %s", c.Status, c.Detail)
	}
	c := QuotaCheck("vCPU", 32, 56, "vCPU", "raise it")
	if c.Status != CheckFail {
		t.Errorf("32 available for 56 needed must fail, got %s", c.Status)
	}
	if c.Fix != "raise it" {
		t.Errorf("a failing quota must carry the remedy, got %q", c.Fix)
	}
	// Unreadable is a warning, not a failure: it is usually a missing read
	// permission, and refusing to proceed on that alone would be worse than
	// proceeding with a caveat.
	if c := QuotaCheck("vCPU", 0, 56, "vCPU", "raise it"); c.Status != CheckWarn {
		t.Errorf("an unreadable limit should warn, got %s", c.Status)
	}
}

// A short quota must offer the change the operator can make now, not only the
// support case: an AWS account at the common 32-vCPU default could not run
// `adhar up` at all, while the same cluster capped at 7 workers fits (2026-10-30).
func TestQuotaFitHintNamesTheCeilingTheQuotaAllows(t *testing.T) {
	// 4-vCPU control plane + 4-vCPU workers in a 32-vCPU quota: 4 + 7 × 4 = 32.
	if got := QuotaFitHint(32, 4, 4, 2, "vCPU"); !strings.Contains(got, "maxWorkers: 7") || !strings.Contains(got, "4 + 7 × 4 = 32 vCPU") {
		t.Errorf("QuotaFitHint(32, 4, 4, 2) = %q, want maxWorkers: 7 with its arithmetic", got)
	}
	// When the starting workers do not fit either, capping cannot help.
	if got := QuotaFitHint(10, 4, 4, 2, "vCPU"); strings.Contains(got, "maxWorkers") || !strings.Contains(got, "only a raise helps") {
		t.Errorf("QuotaFitHint(10, 4, 4, 2) = %q; a cap below the starting size must not be offered", got)
	}
	// Nothing to say without a readable limit or a known worker size.
	if QuotaFitHint(0, 4, 4, 2, "vCPU") != "" || QuotaFitHint(32, 4, 0, 2, "vCPU") != "" {
		t.Error("an unreadable limit or unknown worker size must give no hint")
	}
}

func TestAnyFailedOnlyTripsOnFailures(t *testing.T) {
	if AnyFailed([]Check{{Status: CheckPass}, {Status: CheckWarn}}) {
		t.Error("warnings must not block a create")
	}
	if !AnyFailed([]Check{{Status: CheckPass}, {Status: CheckFail}}) {
		t.Error("a failure must block a create")
	}
	if AnyFailed(nil) {
		t.Error("no checks is not a failure")
	}
}
