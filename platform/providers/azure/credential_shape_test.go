package azure

import (
	"errors"
	"os"
	"strings"
	"testing"
)

const invalidClient = `RESPONSE 401: {"error":"invalid_client","error_description":"AADSTS7000215: Invalid client secret provided."}`

// When Azure says the secret is wrong, say what arrived — without revealing it.
//
// Diagnosing this took three round trips on 2026-10-04, because the error states
// only that the secret is invalid and never what was sent. Everything that
// separates the likely causes is in the LENGTH: an Azure secret value is ~40
// characters, a secret id is a 36-character GUID, and a value the shell
// truncated by expanding an unescaped `$` is short and arbitrary. A length
// discloses nothing, and no part of the value is printed.
func TestCredentialShapeNoteDistinguishesTheLikelyCauses(t *testing.T) {
	for _, tc := range []struct {
		name, secret, want string
	}{
		{"a GUID is a secret id", "a1b2c3d4-5e6f-4a7b-8c9d-0e1f2a3b4c5d", "that is a secret's ID, not its value"},
		{"short means the shell ate it", "abc8Q~dEf1", "SINGLE quotes"},
		{"right length means wrong or stale", "abc8Q~dEf1GhIjKlMnOpQrStUvWxYz0123456789", "az login --tenant"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &Provider{config: &Config{ClientID: "66483720", ClientSecret: tc.secret}}
			note := p.credentialShapeNote(errors.New(invalidClient))
			if !strings.Contains(note, tc.want) {
				t.Errorf("expected guidance %q, got:\n%s", tc.want, note)
			}
			// The secret itself must never appear.
			if strings.Contains(note, tc.secret) {
				t.Error("the note must not contain the secret")
			}
		})
	}
}

// An empty secret is a different failure and must be named as such, rather than
// reported as a zero-length value.
func TestCredentialShapeNoteNamesAnEmptySecret(t *testing.T) {
	p := &Provider{config: &Config{ClientID: "66483720"}}
	note := p.credentialShapeNote(errors.New(invalidClient))
	if !strings.Contains(note, "no client secret was set at all") {
		t.Errorf("an unset secret must be named, got:\n%s", note)
	}
}

// Errors that are not credential rejections must get no note: a quota failure or
// a network reset has nothing to do with the secret's shape, and guessing would
// send the operator down the wrong path.
func TestCredentialShapeNoteStaysQuietOnOtherErrors(t *testing.T) {
	p := &Provider{config: &Config{ClientID: "66483720", ClientSecret: "abc8Q~dEf1GhIjKl"}}
	for _, err := range []error{
		errors.New("connection reset by peer"),
		errors.New("QuotaExceeded: operation could not be completed"),
		nil,
	} {
		if note := p.credentialShapeNote(err); note != "" {
			t.Errorf("no note expected for %v, got:\n%s", err, note)
		}
	}
}

// The length must be stated, since that is the fact the operator cannot
// otherwise obtain — the secret lives in their shell, not in any log.
func TestCredentialShapeNoteStatesTheLength(t *testing.T) {
	p := &Provider{config: &Config{ClientID: "x", ClientSecret: strings.Repeat("a", 12)}}
	if note := p.credentialShapeNote(errors.New(invalidClient)); !strings.Contains(note, "12 characters") {
		t.Errorf("the note must state the length, got:\n%s", note)
	}
}

// The note must be ATTACHED to the authentication error, not merely exist.
//
// Asserted on the source because Authenticate needs a live resourceGroupClient
// to reach its error path. A helper nobody calls is the same as no helper, and
// removing the call site is exactly the regression this guards: the first
// version of these tests exercised credentialShapeNote directly and passed with
// the call site deleted.
func TestAuthenticateAttachesTheCredentialShapeNote(t *testing.T) {
	src, err := readSource("provider.go")
	if err != nil {
		t.Fatalf("reading provider.go: %v", err)
	}
	const want = "p.credentialShapeNote(err)"
	if !strings.Contains(src, want) {
		t.Errorf("Authenticate must append %s to the error it returns — without it the operator "+
			"sees that the secret is invalid and never what was sent", want)
	}
	// It belongs on the authentication failure specifically.
	i := strings.Index(src, "failed to authenticate with Azure")
	if i < 0 {
		t.Fatal("the authentication error wrap has moved; this guard needs updating")
	}
	if !strings.Contains(src[i:min(i+200, len(src))], want) {
		t.Error("the shape note must be attached to the authentication error itself")
	}
}

func readSource(name string) (string, error) {
	b, err := os.ReadFile(name)
	return string(b), err
}

// A right-length secret that Azure rejects is almost always one that was never
// actually replaced — and the reason the replacement failed is usually that `az`
// is pointed at the wrong directory. `az ad sp credential reset` against a
// session with no subscription selected fails with "No subscription found", and
// with `--query password -o tsv` that prints NOTHING: a reset that never ran
// looks exactly like one that did.
//
// Observed live on 2026-10-04 — three consecutive resets appeared to succeed
// while the CLI held tenants 36aa1004… and ac59dfa2…, neither of them the
// tenant the app lives in. So the advice has to name the session first, and
// name the tenant and subscription it should be pointed at.
func TestCredentialShapeNoteNamesTheSessionPrerequisite(t *testing.T) {
	p := &Provider{config: &Config{
		ClientID:       "66483720-42ae-4f05-82a6-e3d75db21412",
		TenantID:       "c95747c3-18ed-485f-9889-34c2ffd38528",
		SubscriptionID: "b8e6d308-0e08-4287-885f-a4766f295f13",
		ClientSecret:   strings.Repeat("a", 40),
	}}
	note := p.credentialShapeNote(errors.New(invalidClient))
	for _, want := range []string{
		// The session comes first: without it the reset cannot work.
		"az login --tenant c95747c3-18ed-485f-9889-34c2ffd38528",
		"az account set --subscription b8e6d308-0e08-4287-885f-a4766f295f13",
		// A verification that is actually authoritative. `az ad app credential
		// list` is NOT: Microsoft Graph lags, and it was seen still reporting only
		// the superseded key minutes after a reset whose secret authenticated —
		// so pointing operators at it manufactures a false "the reset never ran".
		"oauth2/v2.0/token",
		"access_token",
		// The trap itself, stated.
		"silently prints nothing",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("the note must contain %q, got:\n%s", want, note)
		}
	}
}

// The note must NOT send operators to a check that lags.
//
// Microsoft Graph was observed still listing only the superseded key several
// minutes after a reset whose new secret issued a token successfully
// (2026-10-04). Advice built on that list therefore produces a confident, wrong
// conclusion — "no new secret was ever issued" — about a credential that works.
func TestCredentialShapeNoteDoesNotRelyOnALaggingGraphRead(t *testing.T) {
	p := &Provider{config: &Config{
		ClientID: "66483720", TenantID: "c95747c3", SubscriptionID: "b8e6d308",
		ClientSecret: strings.Repeat("a", 40),
	}}
	note := p.credentialShapeNote(errors.New(invalidClient))
	if strings.Contains(note, `--query "[].start"`) {
		t.Error("the note must not present `az ad app credential list` as the verification — " +
			"Graph lags and the list can contradict a credential that authenticates")
	}
	if !strings.Contains(note, "Graph lags") {
		t.Errorf("the note should warn about the lag, since that list is the obvious thing to try; got:\n%s", note)
	}
}
