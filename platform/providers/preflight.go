package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"adhar-io/adhar/platform/types"
)

// Preflight: prove a cloud can actually build the cluster BEFORE anything is
// created.
//
// Why this exists. `adhar up --dry-run` validates the CONFIG and nothing else, so
// it passes cleanly on an account that cannot create a single resource. The
// Provider interface already had ValidatePermissions, but the implementations were
// one shallow call each — AWS's asked only for `ec2:DescribeRegions`, which on a
// real account (2026-09-26) was the ONE EC2 action an Organizations SCP happened
// to allow. So the check passed and the create would have failed partway, leaving
// a half-built VPC and instances billing.
//
// A preflight is therefore only worth having if it probes the calls that actually
// gate a create — reads AND writes — plus the limits a create dies on: quota, and
// the region really offering the instance type asked for.
//
// Kind is deliberately exempt: there is no account, no quota and no permission
// model to check, and `adhar up` skips the stage for it.

// CheckStatus is the outcome of one preflight check.
type CheckStatus string

const (
	// CheckPass means this will not stop the create.
	CheckPass CheckStatus = "pass"
	// CheckWarn means degraded but survivable — a missing DNS zone gives
	// self-signed certificates, for instance, and the platform still comes up.
	CheckWarn CheckStatus = "warn"
	// CheckFail means the create cannot succeed. `adhar up` stops.
	CheckFail CheckStatus = "fail"
)

// Check is one preflight result. Detail says what was OBSERVED and Fix says what
// to do about it: a check that reports a problem without naming the remedy just
// moves the investigation somewhere else.
type Check struct {
	Name   string
	Status CheckStatus
	Detail string
	Fix    string
}

// Failed reports whether this check blocks the create.
func (c Check) Failed() bool { return c.Status == CheckFail }

// Preflighter is an OPTIONAL capability. A provider that implements it replaces
// the generic checks with ones that understand its own API, quotas and error
// vocabulary. Providers that do not are still checked, via ValidatePermissions.
type Preflighter interface {
	Preflight(ctx context.Context, spec *types.ClusterSpec) []Check
}

// RunPreflight returns the checks for a provider, using its own implementation
// when it has one and a generic credential probe otherwise.
//
// The generic path is deliberately modest about what it proves. ValidatePermissions
// makes one API call, so a pass means "the credentials work and the API answers",
// NOT "this create will succeed" — and it says so, rather than implying a
// guarantee it cannot give.
func RunPreflight(ctx context.Context, p Provider, spec *types.ClusterSpec) []Check {
	if pf, ok := p.(Preflighter); ok {
		return pf.Preflight(ctx, spec)
	}
	if err := p.ValidatePermissions(ctx); err != nil {
		return []Check{{
			Name:   "credentials and API access",
			Status: CheckFail,
			Detail: err.Error(),
			Fix:    ExplainAccessError(err),
		}}
	}
	return []Check{{
		Name:   "credentials and API access",
		Status: CheckPass,
		Detail: "the provider answered; note this provider has no deep preflight yet, so quota and per-action permissions are unverified",
	}}
}

// AnyFailed reports whether any check blocks the create.
func AnyFailed(checks []Check) bool {
	for _, c := range checks {
		if c.Failed() {
			return true
		}
	}
	return false
}

// ExplainAccessError turns a provider's access error into the next thing to try.
//
// The AWS case is here because the two failures are worded almost identically and
// need completely different people, and the distinguishing phrase sits at the very
// END of a long line where it is routinely missed:
//
//	... is not authorized to perform: ec2:DescribeVpcs with an explicit deny in a
//	service control policy: arn:aws:organizations::...
//
// An operator reading that sees "not authorized", attaches AdministratorAccess,
// and nothing changes — because a Service Control Policy is a ceiling above IAM
// that the member account cannot raise.
func ExplainAccessError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	// Azure AAD codes come first: they are specific, and the generic AWS cases
	// below match substrings ("AccessDenied") that can appear in an AAD body.
	case strings.Contains(msg, "AADSTS7000215"):
		return "The value in AZURE_CLIENT_SECRET (or providers.azure.clientSecret) is the " +
			"secret's ID, not the secret's VALUE. Azure shows a secret value once, at " +
			"creation, and never again — the GUID the portal and `az ad app credential list` " +
			"display afterwards is the id. Issue a fresh value:\n    " +
			azureCredentialResetHint(msg) +
			"\n    This invalidates the previous secret, so update anything else using it."
	case strings.Contains(msg, "AADSTS7000222"):
		return "The client secret has EXPIRED. Issue a new one:\n    " +
			azureCredentialResetHint(msg) +
			"\n    Azure's default expiry is 1 year (and 3 months via some portal paths)."
	case strings.Contains(msg, "AADSTS700016"), strings.Contains(msg, "AADSTS700215"):
		return "The application id was not found in this tenant: clientId and tenantId " +
			"disagree, or the service principal lives in another directory. Confirm both " +
			"with `az ad sp show --id <clientId> --query '{app:appId,tenant:appOwnerOrganizationId}'`."
	case strings.Contains(msg, "AADSTS90002"):
		return "The tenant id does not exist. Read it from the signed-in session: " +
			"`az account show --query tenantId -o tsv`."
	case strings.Contains(msg, "AADSTS7000112"):
		return "The service principal is DISABLED in the directory. Re-enable it, or create " +
			"a replacement with `az ad sp create-for-rbac`."
	// Civo suspends the whole ACCOUNT for unpaid invoices, and the failure then
	// arrives on whatever call you happened to make — here, a cluster delete.
	// It is worth naming because it looks like nothing else in this list: the
	// credentials are valid, reads keep working, and only writes are refused, so
	// an operator reasonably reads it as a permissions or quota problem and goes
	// looking for the wrong thing.
	//
	// It is also, in hindsight, the explanation for a whole day of symptoms
	// (2026-10-09): four mum1 clusters whose API endpoint went dark while the
	// Civo API still reported ACTIVE/ready=true, the last one with all three
	// instances SHUTOFF. Suspended accounts get their instances powered off.
	// "DatabaseUser" in the code name refers to Civo's own billing records, not
	// to anything this platform runs.
	case strings.Contains(msg, "DatabaseUserSuspendedError"),
		strings.Contains(msg, "account is suspended"):
		return "The CIVO ACCOUNT is suspended for unpaid invoices, so every write is " +
			"refused — create, scale and DELETE alike. Reads still succeed, which is why " +
			"this can look like a permissions or quota fault. Nothing in this " +
			"configuration will change it: settle the invoice at " +
			"https://dashboard.civo.com/billing, then ask Civo to re-enable the account " +
			"and re-run. Note that suspension also POWERS OFF running instances, so a " +
			"cluster whose API has gone unreachable while the provider still reports it " +
			"ACTIVE/ready=true is the expected appearance of this, not a cluster fault."
	case strings.Contains(msg, "explicit deny in a service control policy"):
		return "An AWS Organizations SCP denies this, which no IAM policy in this " +
			"account can override — attaching AdministratorAccess will not help. " +
			"Amend the policy from the ORGANIZATION'S MANAGEMENT ACCOUNT " +
			"(aws organizations describe-policy --policy-id <id>). Re-test with a " +
			"repeated sample: while an SCP is being edited, calls pass briefly and " +
			"then go back to denied."
	case strings.Contains(msg, "no identity-based policy allows"),
		strings.Contains(msg, "UnauthorizedOperation"),
		strings.Contains(msg, "AccessDenied"):
		return "A permission is missing on the identity itself. Add it in THIS " +
			"account; see the provider guide for the exact policy."
	case strings.Contains(msg, "ExpiredToken"), strings.Contains(msg, "InvalidClientTokenId"):
		return "The credentials are expired or wrong. Re-issue them and retry."
	default:
		return ""
	}
}

// QuotaCheck builds a check from a limit and what the cluster will need at its
// MAXIMUM size, not its starting size.
//
// Sizing to the starting node count is the trap: the cluster boots, then the
// autoscaler tries to add the capacity the platform actually needs and hits a wall
// it cannot report usefully.
func QuotaCheck(name string, limit, need float64, unit, raiseWith string) Check {
	switch {
	case limit <= 0:
		return Check{
			Name:   name,
			Status: CheckWarn,
			Detail: fmt.Sprintf("could not read the limit; %s needs %.0f %s", name, need, unit),
			Fix:    raiseWith,
		}
	case limit < need:
		return Check{
			Name:   name,
			Status: CheckFail,
			Detail: fmt.Sprintf("limit is %.0f %s, the cluster needs %.0f at full size", limit, unit, need),
			Fix:    raiseWith,
		}
	default:
		return Check{
			Name:   name,
			Status: CheckPass,
			Detail: fmt.Sprintf("%.0f %s available, %.0f needed at full size", limit, unit, need),
		}
	}
}

// azureCredentialResetHint builds the exact command to issue a new secret.
//
// AAD states the offending application id inside the error body ("for a secret
// added to app '<guid>'"), so the remedy can name it instead of leaving the
// operator to find which of several principals is at fault.
func azureCredentialResetHint(msg string) string {
	appID := "<clientId>"
	if m := azureAppIDPattern.FindStringSubmatch(msg); len(m) == 2 {
		appID = m[1]
	}
	return "az ad sp credential reset --id " + appID + " --query password -o tsv"
}

// azureAppIDPattern matches the app id AAD quotes in its error body.
//
// The wording differs per code — 7000215 says "for a secret added to app 'x'"
// while 7000222 says "client secret keys for app 'x' are expired" — so match on
// the quoted guid after "app" rather than on either sentence.
var azureAppIDPattern = regexp.MustCompile(`app '([0-9a-fA-F-]{36})'`)
