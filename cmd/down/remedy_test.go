package down

// Teardown must translate provider errors, not just print them.
//
// ExplainAccessError was consulted only on the `adhar up` path. Teardown printed
// the provider's raw message — and teardown is where translation matters most,
// because a delete that fails leaves infrastructure billing while the operator
// reads an error that points at the wrong thing.
//
// The case that exposed it (2026-10-09): Civo suspended the account for unpaid
// invoices and refused every write. Reads kept working, so `adhar down` reported
//
//	teardown failed for: dev: failed to delete Civo cluster dc72b5ee-…:
//	DatabaseUserSuspendedError: Your account is suspended …
//
// which reads like a credentials or quota problem. It is neither, nothing in the
// configuration can change it, and the same suspension had been powering the
// cluster's instances off for hours — which is what made four clusters look like
// they had failed on their own.

import (
	"errors"
	"os"
	"strings"
	"testing"

	pfactory "adhar-io/adhar/platform/providers"
)

func TestTeardownAttachesTheRemedyToFailures(t *testing.T) {
	raw, err := os.ReadFile("down.go")
	if err != nil {
		t.Fatalf("reading down.go: %v", err)
	}
	src := string(raw)
	// Comments stripped: the note above quotes the identifier being searched for.
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	code := b.String()

	if !strings.Contains(code, "pfactory.ExplainAccessError(err)") {
		t.Error("the teardown failure path does not consult ExplainAccessError, so a provider error " +
			"that needs translating — a suspended account, an SCP denial, an expired secret — is " +
			"reported raw at the moment infrastructure is left behind")
	}
}

// A suspended Civo account must be named as such, and the advice must not send
// the operator to the configuration.
func TestCivoSuspensionIsExplained(t *testing.T) {
	err := errors.New("failed to delete Civo cluster dc72b5ee-fc21-4a8c-9035-9abd6621c614: " +
		"DatabaseUserSuspendedError: Your account is suspended due to one or more unpaid invoices, " +
		"please update your credit card details")

	remedy := pfactory.ExplainAccessError(err)
	if remedy == "" {
		t.Fatal("a suspended-account error produced no explanation; it reads as a permissions or " +
			"quota fault and sends the operator looking in the wrong place")
	}
	for _, want := range []string{"suspended", "invoice"} {
		if !strings.Contains(strings.ToLower(remedy), want) {
			t.Errorf("the explanation does not mention %q: %s", want, remedy)
		}
	}
	// The operationally important part: it also explains the symptom that had
	// been mistaken for four separate cluster failures.
	if !strings.Contains(remedy, "POWERS OFF") {
		t.Error("the explanation does not say that suspension powers off running instances — the " +
			"symptom that made four clusters look like they died on their own")
	}
}

// The generic AWS cases must not swallow it: "account is suspended" contains
// none of their markers, but ordering in a switch is easy to get wrong.
func TestSuspensionIsNotMistakenForAnIAMDenial(t *testing.T) {
	remedy := pfactory.ExplainAccessError(errors.New("DatabaseUserSuspendedError: account is suspended"))
	if strings.Contains(remedy, "IAM") || strings.Contains(remedy, "service control policy") {
		t.Errorf("a suspended account was explained as an AWS permissions problem: %s", remedy)
	}
}
