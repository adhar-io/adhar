package auth

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func TestSessionRoundTrip(t *testing.T) {
	t.Setenv("ADHAR_CONFIG_DIR", t.TempDir())

	// Not logged in: nil, nil.
	s, err := loadSession()
	if err != nil || s != nil {
		t.Fatalf("expected empty session, got %v, %v", s, err)
	}

	kc := keycloak{Issuer: "https://kc.example/realms/adhar", ClientID: "adhar-cli"}
	tr := &tokenResponse{AccessToken: "at", RefreshToken: "rt", ExpiresIn: 300, RefreshExpiresIn: 1800}
	if err := saveSession(sessionFromTokenResponse(kc, "dev", tr)); err != nil {
		t.Fatal(err)
	}

	s, err = loadSession()
	if err != nil {
		t.Fatal(err)
	}
	if s.Username != "dev" || s.AccessToken != "at" || s.ClientID != "adhar-cli" {
		t.Errorf("unexpected session: %+v", s)
	}
	if time.Until(s.AccessExpiry) <= 0 || time.Until(s.RefreshExpiry) <= time.Until(s.AccessExpiry) {
		t.Errorf("expiries not ordered: %+v", s)
	}

	if err := deleteSession(); err != nil {
		t.Fatal(err)
	}
	if err := deleteSession(); err != nil {
		t.Errorf("double delete must be a no-op, got %v", err)
	}
	if s, _ := loadSession(); s != nil {
		t.Error("session survived delete")
	}
}

func TestParseClaims(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"sub":                "abc",
		"preferred_username": "dev",
		"email":              "dev@example.com",
		"groups":             []string{"platform-developer"},
		"iss":                "https://kc.example/realms/adhar",
		"exp":                1234567890,
		"realm_access":       map[string]any{"roles": []string{"offline_access"}},
	})
	token := "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"

	c, err := parseClaims(token)
	if err != nil {
		t.Fatal(err)
	}
	if c.PreferredUsername != "dev" || c.Email != "dev@example.com" {
		t.Errorf("unexpected claims: %+v", c)
	}
	if len(c.Groups) != 1 || c.Groups[0] != "platform-developer" {
		t.Errorf("groups not parsed: %+v", c.Groups)
	}
	if len(c.RealmAccess.Roles) != 1 {
		t.Errorf("realm roles not parsed: %+v", c.RealmAccess)
	}

	if _, err := parseClaims("not-a-jwt"); err == nil {
		t.Error("expected error for malformed token")
	}
}
