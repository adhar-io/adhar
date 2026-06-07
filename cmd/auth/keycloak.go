/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the file at

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
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Default Keycloak endpoints for the local Adhar platform. Override with the
// persistent flags on the auth command.
const (
	defaultIssuer      = "https://adhar.localtest.me:8443/keycloak/realms/adhar"
	defaultClientID    = "admin-cli"
	defaultAdminAPIURL = "https://adhar.localtest.me:8443/keycloak"
	defaultRealm       = "adhar"
)

// keycloak holds the resolved connection settings shared by auth subcommands.
type keycloak struct {
	Issuer     string
	AdminURL   string
	Realm      string
	ClientID   string
	AdminToken string
	Insecure   bool
}

// settings builds a keycloak config from the global auth flags.
func settings() keycloak {
	realm := kcRealm
	if realm == "" {
		realm = defaultRealm
	}
	clientID := kcClientID
	if clientID == "" {
		clientID = defaultClientID
	}
	return keycloak{
		Issuer:     strings.TrimRight(kcIssuer, "/"),
		AdminURL:   strings.TrimRight(kcAdminURL, "/"),
		Realm:      realm,
		ClientID:   clientID,
		AdminToken: kcAdminToken,
		Insecure:   kcInsecure,
	}
}

// httpClient returns an HTTP client honoring the timeout and --insecure flag.
// The local platform uses a self-signed certificate, so --insecure is commonly
// required.
func (k keycloak) httpClient() *http.Client {
	tr := &http.Transport{}
	if k.Insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 - opt-in via --insecure for self-signed dev certs
	}
	return &http.Client{Timeout: 30 * time.Second, Transport: tr}
}

// tokenResponse models the OIDC token endpoint response.
type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshExpiresIn int    `json:"refresh_expires_in"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// tokenEndpoint derives the realm token endpoint from the issuer URL.
func (k keycloak) tokenEndpoint() string {
	return k.Issuer + "/protocol/openid-connect/token"
}

// passwordGrant performs the OIDC Resource Owner Password Credentials grant and
// returns the token response.
func (k keycloak) passwordGrant(ctx context.Context, username, password string) (*tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "password")
	form.Set("client_id", k.ClientID)
	form.Set("username", username)
	form.Set("password", password)
	if kcClientSecret != "" {
		form.Set("client_secret", kcClientSecret)
	}
	return k.postToken(ctx, form)
}

// clientCredentialsGrant performs the OIDC client_credentials grant.
func (k keycloak) clientCredentialsGrant(ctx context.Context) (*tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", k.ClientID)
	if kcClientSecret != "" {
		form.Set("client_secret", kcClientSecret)
	}
	return k.postToken(ctx, form)
}

func (k keycloak) postToken(ctx context.Context, form url.Values) (*tokenResponse, error) {
	endpoint := k.tokenEndpoint()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := k.httpClient().Do(req)
	if err != nil {
		return nil, unreachable(endpoint, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("decoding token response (%s): %w", resp.Status, err)
	}
	if tr.Error != "" {
		return nil, fmt.Errorf("keycloak token error: %s: %s", tr.Error, tr.ErrorDescription)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("keycloak token endpoint returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return &tr, nil
}

// bearer resolves the admin bearer token to use for Admin REST calls. If
// --admin-token is set it is used directly; otherwise a client_credentials grant
// is attempted (works when admin-cli has a service account / secret configured).
func (k keycloak) bearer(ctx context.Context) (string, error) {
	if k.AdminToken != "" {
		return k.AdminToken, nil
	}
	tr, err := k.clientCredentialsGrant(ctx)
	if err != nil {
		return "", fmt.Errorf("no --admin-token supplied and client_credentials grant failed: %w\n  hint: pass --admin-token <token> (e.g. from `adhar auth login` or `kcadm.sh`)", err)
	}
	return tr.AccessToken, nil
}

// adminGet issues an authenticated GET against the Keycloak Admin REST API and
// decodes the JSON array/object into out. path is relative to
// /admin/realms/{realm}, e.g. "/users".
func (k keycloak) adminGet(ctx context.Context, path string, out interface{}) error {
	token, err := k.bearer(ctx)
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/admin/realms/%s%s", k.AdminURL, url.PathEscape(k.Realm), path)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := k.httpClient().Do(req)
	if err != nil {
		return unreachable(endpoint, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("keycloak admin API returned %s (token lacks realm-admin permissions?)", resp.Status)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("keycloak admin API returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decoding admin API response: %w", err)
	}
	return nil
}

// unreachable wraps a connection error with a friendly hint.
func unreachable(endpoint string, err error) error {
	return fmt.Errorf("could not reach %s: %w\n  hint: Keycloak may not be running, or the URL/TLS is wrong. For the local self-signed cert add --insecure, and override the endpoint with --issuer / --admin-url", endpoint, err)
}
