/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/client-go/util/homedir"
)

// newFormRequest builds a POST request with form-encoded body.
func newFormRequest(ctx context.Context, endpoint string, form url.Values) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req, nil
}

// storedSession is the persisted login state written by `adhar auth login` and
// consumed by token/whoami/logout. It lives at ~/.adhar/credentials.json with
// 0600 permissions.
type storedSession struct {
	Issuer        string    `json:"issuer"`
	ClientID      string    `json:"clientId"`
	Username      string    `json:"username"`
	AccessToken   string    `json:"accessToken"`
	RefreshToken  string    `json:"refreshToken"`
	AccessExpiry  time.Time `json:"accessExpiry"`
	RefreshExpiry time.Time `json:"refreshExpiry"`
	Insecure      bool      `json:"insecure,omitempty"`
}

// credentialsPath returns the session file location, honoring ADHAR_CONFIG_DIR
// for tests and non-standard setups.
func credentialsPath() string {
	dir := os.Getenv("ADHAR_CONFIG_DIR")
	if dir == "" {
		dir = filepath.Join(homedir.HomeDir(), ".adhar")
	}
	return filepath.Join(dir, "credentials.json")
}

// saveSession persists the session with owner-only permissions.
func saveSession(s *storedSession) error {
	path := credentialsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("writing credentials: %w", err)
	}
	return nil
}

// loadSession reads the persisted session; a nil session with nil error means
// "not logged in".
func loadSession() (*storedSession, error) {
	b, err := os.ReadFile(credentialsPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading credentials: %w", err)
	}
	var s storedSession
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("decoding credentials (delete %s and log in again): %w", credentialsPath(), err)
	}
	return &s, nil
}

// deleteSession removes the persisted session. Missing file is not an error.
func deleteSession() error {
	err := os.Remove(credentialsPath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// sessionFromTokenResponse builds a storedSession from a token-endpoint reply.
func sessionFromTokenResponse(kc keycloak, username string, tr *tokenResponse) *storedSession {
	now := time.Now()
	return &storedSession{
		Issuer:        kc.Issuer,
		ClientID:      kc.ClientID,
		Username:      username,
		AccessToken:   tr.AccessToken,
		RefreshToken:  tr.RefreshToken,
		AccessExpiry:  now.Add(time.Duration(tr.ExpiresIn) * time.Second),
		RefreshExpiry: now.Add(time.Duration(tr.RefreshExpiresIn) * time.Second),
		Insecure:      kc.Insecure,
	}
}

// jwtClaims is the subset of ID/access-token claims the CLI presents.
type jwtClaims struct {
	Subject           string   `json:"sub"`
	PreferredUsername string   `json:"preferred_username"`
	Email             string   `json:"email"`
	Groups            []string `json:"groups"`
	Issuer            string   `json:"iss"`
	Expiry            int64    `json:"exp"`
	RealmAccess       struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

// parseClaims decodes the (unverified) payload segment of a JWT. The CLI only
// uses this for display — authorization decisions are the server's job, so
// signature verification is deliberately not done here.
func parseClaims(token string) (*jwtClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("not a JWT (expected 3 segments, got %d)", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decoding JWT payload: %w", err)
	}
	var c jwtClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, fmt.Errorf("parsing JWT claims: %w", err)
	}
	return &c, nil
}

// refreshGrant exchanges a refresh token for a new token pair.
func (k keycloak) refreshGrant(ctx context.Context, refreshToken string) (*tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", k.ClientID)
	form.Set("refresh_token", refreshToken)
	if kcClientSecret != "" {
		form.Set("client_secret", kcClientSecret)
	}
	return k.postToken(ctx, form)
}

// endSession invalidates the refresh token at Keycloak's logout endpoint.
func (k keycloak) endSession(ctx context.Context, refreshToken string) error {
	form := url.Values{}
	form.Set("client_id", k.ClientID)
	form.Set("refresh_token", refreshToken)
	if kcClientSecret != "" {
		form.Set("client_secret", kcClientSecret)
	}
	endpoint := k.Issuer + "/protocol/openid-connect/logout"
	req, err := newFormRequest(ctx, endpoint, form)
	if err != nil {
		return err
	}
	resp, err := k.httpClient().Do(req)
	if err != nil {
		return unreachable(endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("keycloak logout endpoint returned %s", resp.Status)
	}
	return nil
}

// currentSession returns a session with a valid access token, transparently
// refreshing (and re-persisting) when the access token has expired. Returns a
// helpful error when not logged in or the refresh token has expired too.
func currentSession(ctx context.Context) (*storedSession, error) {
	s, err := loadSession()
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("not logged in — run `adhar auth login <username>` first")
	}
	// 10s of slack so a token that expires mid-request doesn't get used.
	if time.Until(s.AccessExpiry) > 10*time.Second {
		return s, nil
	}
	if s.RefreshToken == "" || time.Now().After(s.RefreshExpiry) {
		return nil, fmt.Errorf("session expired — run `adhar auth login %s` again", s.Username)
	}
	kc := settings()
	// Refresh against the issuer/client the session was created with, not
	// whatever the flags currently default to.
	kc.Issuer = s.Issuer
	kc.ClientID = s.ClientID
	kc.Insecure = kc.Insecure || s.Insecure
	tr, err := kc.refreshGrant(ctx, s.RefreshToken)
	if err != nil {
		return nil, fmt.Errorf("refreshing session (log in again if this persists): %w", err)
	}
	refreshed := sessionFromTokenResponse(kc, s.Username, tr)
	if err := saveSession(refreshed); err != nil {
		return nil, err
	}
	return refreshed, nil
}
