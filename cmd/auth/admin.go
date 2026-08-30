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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// adminDo issues an authenticated request against the Keycloak Admin REST API
// with an optional JSON body, returning the status code, response body and the
// Location header (Keycloak returns the new resource's URL there on 201 Create).
// path is relative to /admin/realms/{realm}, e.g. "/users".
//
// The read counterpart (adminGet) lives in keycloak.go; this is the write side
// (POST/PUT/DELETE) shared by the user/group/role/provider subcommands. All of
// them go through the same admin bearer token the REAL list commands already use.
func (k keycloak) adminDo(ctx context.Context, method, path string, body interface{}) (int, []byte, string, error) {
	token, err := k.bearer(ctx)
	if err != nil {
		return 0, nil, "", err
	}
	endpoint := fmt.Sprintf("%s/admin/realms/%s%s", k.AdminURL, url.PathEscape(k.Realm), path)

	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, "", fmt.Errorf("encoding request body: %w", err)
		}
		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return 0, nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := k.httpClient().Do(req)
	if err != nil {
		return 0, nil, "", unreachable(endpoint, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, respBody, resp.Header.Get("Location"), nil
}

// adminWrite is a convenience wrapper that treats any non-2xx as an error with a
// helpful message, and returns the Location header for create calls.
func (k keycloak) adminWrite(ctx context.Context, method, path string, body interface{}) (string, error) {
	status, respBody, location, err := k.adminDo(ctx, method, path, body)
	if err != nil {
		return "", err
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "", fmt.Errorf("keycloak admin API returned %d (token lacks realm-admin permissions?)", status)
	case status == http.StatusConflict:
		return "", fmt.Errorf("already exists (keycloak returned 409 Conflict): %s", strings.TrimSpace(string(respBody)))
	case status < 200 || status >= 300:
		return "", fmt.Errorf("keycloak admin API returned %d: %s", status, strings.TrimSpace(string(respBody)))
	}
	return location, nil
}

// idFromLocation extracts the trailing path segment (the created resource id)
// from a Keycloak Location header.
func idFromLocation(location string) string {
	if location == "" {
		return ""
	}
	parts := strings.Split(strings.TrimRight(location, "/"), "/")
	return parts[len(parts)-1]
}

// userIDByUsername resolves a username to its Keycloak user id (exact match).
func (k keycloak) userIDByUsername(ctx context.Context, username string) (string, error) {
	var users []kcUser
	path := fmt.Sprintf("/users?exact=true&username=%s", url.QueryEscape(username))
	if err := k.adminGet(ctx, path, &users); err != nil {
		return "", err
	}
	for _, u := range users {
		if strings.EqualFold(u.Username, username) {
			return u.ID, nil
		}
	}
	return "", fmt.Errorf("user %q not found in realm %s", username, k.Realm)
}

// groupIDByName resolves a top-level group name to its Keycloak group id.
func (k keycloak) groupIDByName(ctx context.Context, name string) (string, error) {
	var groups []kcGroup
	path := fmt.Sprintf("/groups?search=%s", url.QueryEscape(name))
	if err := k.adminGet(ctx, path, &groups); err != nil {
		return "", err
	}
	var match func(gs []kcGroup) string
	match = func(gs []kcGroup) string {
		for _, g := range gs {
			if strings.EqualFold(g.Name, name) {
				return g.ID
			}
			if id := match(g.SubGroups); id != "" {
				return id
			}
		}
		return ""
	}
	if id := match(groups); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("group %q not found in realm %s", name, k.Realm)
}

// realmRoleByName fetches a realm role's full representation (needed as the body
// of a role-mapping request, which requires id+name).
func (k keycloak) realmRoleByName(ctx context.Context, name string) (kcRole, error) {
	var r kcRole
	if err := k.adminGetOne(ctx, "/roles/"+url.PathEscape(name), &r); err != nil {
		return kcRole{}, fmt.Errorf("realm role %q: %w", name, err)
	}
	return r, nil
}

// assignRealmRole grants the named realm role to a user id.
func (k keycloak) assignRealmRole(ctx context.Context, userID, roleName string) error {
	role, err := k.realmRoleByName(ctx, roleName)
	if err != nil {
		return err
	}
	_, err = k.adminWrite(ctx, http.MethodPost, fmt.Sprintf("/users/%s/role-mappings/realm", userID),
		[]kcRole{role})
	return err
}

// revokeRealmRole removes the named realm role from a user id.
func (k keycloak) revokeRealmRole(ctx context.Context, userID, roleName string) error {
	role, err := k.realmRoleByName(ctx, roleName)
	if err != nil {
		return err
	}
	_, err = k.adminWrite(ctx, http.MethodDelete, fmt.Sprintf("/users/%s/role-mappings/realm", userID),
		[]kcRole{role})
	return err
}

// clientSessionStats returns per-client active/offline session counts for the
// realm (the realm-wide equivalent of "who is logged in").
func (k keycloak) clientSessionStats(ctx context.Context) ([]kcClientSessionStat, error) {
	var stats []kcClientSessionStat
	if err := k.adminGet(ctx, "/client-session-stats", &stats); err != nil {
		return nil, err
	}
	return stats, nil
}

// getJSON GETs a single admin resource and decodes it, returning a clear error
// (including 404) rather than the raw body.
func (k keycloak) adminGetOne(ctx context.Context, path string, out interface{}) error {
	status, body, _, err := k.adminDo(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return fmt.Errorf("not found (keycloak returned 404)")
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("keycloak admin API returned %d: %s", status, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decoding admin API response: %w", err)
	}
	return nil
}
