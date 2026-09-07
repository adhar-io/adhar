package get

import (
	"bytes"
	"strings"
	"testing"
)

func sampleEntries() []SecretEntry {
	return []SecretEntry{
		{Icon: "🚀", Service: "ArgoCD", Username: "admin", Password: "short"},
		{Icon: "🧭", Service: "Adhar Console (SSO client)", Username: "adhar-console", Password: strings.Repeat("x", 90)},
	}
}

func TestRenderSecretsTableNeverTruncates(t *testing.T) {
	long := strings.Repeat("x", 90)
	out := renderSecretsTable(sampleEntries(), 0) // width 0: not a terminal, always tabular
	if !strings.Contains(out, long) {
		t.Fatalf("90-char password must be printed in full:\n%s", out)
	}
	if strings.Contains(out, "...") {
		t.Fatalf("no value may be truncated:\n%s", out)
	}
	for _, want := range []string{"SERVICE", "USERNAME", "PASSWORD", "🚀 ArgoCD", "adhar-console"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in table:\n%s", want, out)
		}
	}
}

func TestRenderSecretsTableFallsBackToCardsWhenNarrow(t *testing.T) {
	out := renderSecretsTable(sampleEntries(), 60)
	if !strings.Contains(out, "   password  "+strings.Repeat("x", 90)) {
		t.Fatalf("narrow terminal should render cards with the full password:\n%s", out)
	}
	if strings.Contains(out, "SERVICE") {
		t.Errorf("cards must not carry the table header:\n%s", out)
	}
}

func TestSecretsMachineFormats(t *testing.T) {
	var js bytes.Buffer
	if err := writeSecretsJSON(&js, sampleEntries()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(js.String(), `"service": "ArgoCD"`) || !strings.Contains(js.String(), `"password": "short"`) {
		t.Errorf("unexpected json:\n%s", js.String())
	}
	var env bytes.Buffer
	if err := writeSecretsEnv(&env, sampleEntries()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`export ARGOCD_USERNAME="admin"`, `export ARGOCD_PASSWORD="short"`, `export ADHAR_CONSOLE_SSO_CLIENT_PASSWORD="`} {
		if !strings.Contains(env.String(), want) {
			t.Errorf("missing %q in env output:\n%s", want, env.String())
		}
	}
}

func TestFindEntry(t *testing.T) {
	e, ok := findEntry(sampleEntries(), "argocd")
	if !ok || e.Service != "ArgoCD" {
		t.Fatalf("case-insensitive exact match failed: %+v %v", e, ok)
	}
	e, ok = findEntry(sampleEntries(), "console")
	if !ok || e.Username != "adhar-console" {
		t.Fatalf("substring match failed: %+v %v", e, ok)
	}
	if _, ok := findEntry(sampleEntries(), "vault"); ok {
		t.Fatal("unknown service must not match")
	}
}
