package provider

import (
	"errors"
	"strings"
	"testing"
)

// The one actionable sentence must not be buried in the SDK's error body.
//
// A live Azure run on 2026-10-04 failed with a 40-line azidentity dump — the
// POST URL, a 401 banner, the whole JSON body, trace and correlation ids — and
// then the failure panel reprinted all of it, overflowing its own border. The
// sentence that mattered sat in the middle of the JSON:
//
//	AADSTS7000215: Invalid client secret provided. Ensure the secret being sent
//	in the request is the client secret value, not the client secret ID, for a
//	secret added to app '66483720-...'
//
// This is the single most common Azure credential mistake, because the value is
// displayed once at creation and the GUID shown everywhere afterwards is the id.
func TestExplainAccessErrorNamesTheSecretIDMistake(t *testing.T) {
	err := errors.New(`ClientSecretCredential authentication failed. POST https://login.microsoftonline.com/c95747c3/oauth2/v2.0/token
RESPONSE 401: 401 Unauthorized
{"error":"invalid_client","error_description":"AADSTS7000215: Invalid client secret provided. Ensure the secret being sent in the request is the client secret value, not the client secret ID, for a secret added to app '66483720-42ae-4f05-82a6-e3d75db21412'. Trace ID: 1ef53886","error_codes":[7000215]}`)

	fix := ExplainAccessError(err)
	if fix == "" {
		t.Fatal("AADSTS7000215 must be explained — otherwise the remedy stays buried in the JSON body")
	}
	for _, want := range []string{
		"secret's ID, not the secret's VALUE",
		"az ad sp credential reset",
		// The app id is quoted in the body, so the command can be copy-pasted
		// rather than leaving the operator to guess which principal is at fault.
		"66483720-42ae-4f05-82a6-e3d75db21412",
		// Resetting invalidates the old secret; anything else using it breaks.
		"invalidates the previous secret",
	} {
		if !strings.Contains(fix, want) {
			t.Errorf("the explanation must contain %q, got:\n%s", want, fix)
		}
	}
}

// An expired secret looks identical to a wrong one at the call site but needs a
// different sentence, and the expiry default is the thing people forget.
func TestExplainAccessErrorNamesAnExpiredSecret(t *testing.T) {
	err := errors.New(`{"error":"invalid_client","error_description":"AADSTS7000222: The provided client secret keys for app '66483720-42ae-4f05-82a6-e3d75db21412' are expired."}`)
	fix := ExplainAccessError(err)
	if !strings.Contains(fix, "EXPIRED") || !strings.Contains(fix, "az ad sp credential reset") {
		t.Errorf("an expired secret must be named as such with the reset command, got:\n%s", fix)
	}
	if !strings.Contains(fix, "66483720-42ae-4f05-82a6-e3d75db21412") {
		t.Errorf("the reset command must name the app id from the error body, got:\n%s", fix)
	}
}

// clientId/tenantId disagreement and a bad tenant are distinct failures with
// distinct fixes, and neither is a permissions problem — which is what the
// generic "AccessDenied" branch below would otherwise suggest.
func TestExplainAccessErrorDistinguishesDirectoryFailures(t *testing.T) {
	for _, tc := range []struct{ code, want string }{
		{"AADSTS700016", "az ad sp show"},
		{"AADSTS90002", "az account show --query tenantId"},
		{"AADSTS7000112", "DISABLED"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			fix := ExplainAccessError(errors.New(`{"error_description":"` + tc.code + `: something"}`))
			if !strings.Contains(fix, tc.want) {
				t.Errorf("%s must suggest %q, got:\n%s", tc.code, tc.want, fix)
			}
		})
	}
}

// Without the app id in the body the hint must still be a usable command, with
// an obvious placeholder rather than an empty --id.
func TestExplainAccessErrorFallsBackWhenNoAppIDIsQuoted(t *testing.T) {
	fix := ExplainAccessError(errors.New(`{"error_description":"AADSTS7000215: Invalid client secret provided."}`))
	if !strings.Contains(fix, "--id <clientId>") {
		t.Errorf("expected a placeholder app id, got:\n%s", fix)
	}
}

// The AWS branches must keep working: an AAD body can contain words like
// "AccessDenied", so the Azure cases are matched first and must not shadow them.
func TestExplainAccessErrorStillExplainsTheAWSSCP(t *testing.T) {
	err := errors.New("is not authorized to perform: ec2:DescribeVpcs with an explicit deny in a service control policy")
	if fix := ExplainAccessError(err); !strings.Contains(fix, "MANAGEMENT ACCOUNT") {
		t.Errorf("the AWS SCP explanation must survive, got:\n%s", fix)
	}
}

// An unrecognised error must produce nothing, so the caller prints the raw error
// instead of a confident but wrong remedy.
func TestExplainAccessErrorStaysQuietOnUnknownErrors(t *testing.T) {
	if fix := ExplainAccessError(errors.New("connection reset by peer")); fix != "" {
		t.Errorf("an unknown error must not be explained, got:\n%s", fix)
	}
	if fix := ExplainAccessError(nil); fix != "" {
		t.Errorf("nil must explain nothing, got:\n%s", fix)
	}
}
