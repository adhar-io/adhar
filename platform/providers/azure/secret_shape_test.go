package azure

import (
	"strings"
	"testing"
)

// A client secret that is a bare GUID is the secret's ID, and must be rejected
// without a round trip to Azure.
//
// Azure displays a secret value exactly once, at creation. Afterwards the portal
// and `az ad app credential list` show only the id — so the id is what gets
// copied. Azure's own answer costs a network call and arrives like this, with the
// one useful sentence in the middle of a JSON body (live, 2026-10-04):
//
//	RESPONSE 401: 401 Unauthorized
//	{"error":"invalid_client","error_description":"AADSTS7000215: Invalid client
//	secret provided. Ensure the secret being sent in the request is the client
//	secret value, not the client secret ID, for a secret added to app '6648…'
//
// Secret VALUES are ~40 characters of mixed-case base64 with punctuation and are
// never GUID-shaped, so this check is free and cannot misfire on a real secret.
func TestCredentialsRejectASecretIDBeforeCallingAzure(t *testing.T) {
	cfg := &Config{
		TenantID: "c95747c3-18ed-485f-9889-34c2ffd38528",
		ClientID: "66483720-42ae-4f05-82a6-e3d75db21412",
		// The id of a secret, not its value.
		ClientSecret: "a1b2c3d4-5e6f-4a7b-8c9d-0e1f2a3b4c5d",
	}
	_, err := createAzureCredentials(cfg)
	if err == nil {
		t.Fatal("a GUID-shaped client secret must be refused before any network call")
	}
	for _, want := range []string{
		"secret's ID and not its VALUE",
		"az ad sp credential reset",
		// Name the app, so the command can be pasted as-is.
		"66483720-42ae-4f05-82a6-e3d75db21412",
		// Secret values routinely contain $ and other shell-active characters,
		// which is the next mistake after this one.
		"SINGLE quotes",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must contain %q, got:\n%v", want, err)
		}
	}
}

// A real secret value must pass. These are shaped like Azure's own output and
// include the punctuation that makes them look nothing like a GUID.
func TestCredentialsAcceptRealSecretValues(t *testing.T) {
	for _, secret := range []string{
		"abc8Q~dEf1GhIjKlMnOpQrStUvWxYz0123456789",
		"Xy2_~qW3eR4tY5uI6oP7aS8dF9gH0jK1lZ2xC3vB",
		// Short and odd, but still not a GUID — the check must not guess.
		"s3cret",
	} {
		cfg := &Config{TenantID: "tenant", ClientID: "client", ClientSecret: secret}
		if _, err := createAzureCredentials(cfg); err != nil {
			t.Errorf("a real secret value must be accepted, got for %q: %v", secret, err)
		}
	}
}

// Braces are how some tools render a GUID, and a braced id is the same mistake.
func TestSecretShapeDetectsBracedGUIDs(t *testing.T) {
	if !secretLooksLikeAnID("{a1b2c3d4-5e6f-4a7b-8c9d-0e1f2a3b4c5d}") {
		t.Error("a braced GUID is still a secret id")
	}
	// Surrounding whitespace from a copy-paste must not hide it either.
	if !secretLooksLikeAnID("  a1b2c3d4-5e6f-4a7b-8c9d-0e1f2a3b4c5d\n") {
		t.Error("an untrimmed GUID is still a secret id")
	}
	if secretLooksLikeAnID("") {
		t.Error("an empty secret is a different failure and must not be reported as an id")
	}
}
