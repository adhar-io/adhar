package keycloak

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"time"
)

// Client represents a Keycloak client
type Client struct {
	BaseURL      string
	Realm        string
	ClientID     string
	ClientSecret string
	HTTPClient   *http.Client
	AccessToken  string
}

// User represents a Keycloak user
type User struct {
	ID              string              `json:"id,omitempty"`
	Username        string              `json:"username"`
	Email           string              `json:"email,omitempty"`
	FirstName       string              `json:"firstName,omitempty"`
	LastName        string              `json:"lastName,omitempty"`
	Enabled         bool                `json:"enabled"`
	EmailVerified   bool                `json:"emailVerified,omitempty"`
	Attributes      map[string][]string `json:"attributes,omitempty"`
	Groups          []string            `json:"groups,omitempty"`
	RequiredActions []string            `json:"requiredActions,omitempty"`
	Credentials     []Credential        `json:"credentials,omitempty"`
}

// Credential represents user credentials
type Credential struct {
	Type      string `json:"type"`
	Value     string `json:"value,omitempty"`
	Temporary bool   `json:"temporary"`
}

// Group represents a Keycloak group
type Group struct {
	ID         string            `json:"id,omitempty"`
	Name       string            `json:"name"`
	Path       string            `json:"path,omitempty"`
	SubGroups  []Group           `json:"subGroups,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
	RealmRoles []string          `json:"realmRoles,omitempty"`
}

// Role represents a Keycloak role
type Role struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Composite   bool   `json:"composite,omitempty"`
	ClientRole  bool   `json:"clientRole,omitempty"`
	ContainerID string `json:"containerId,omitempty"`
}

// TokenResponse represents the OAuth2 token response
type TokenResponse struct {
	AccessToken      string `json:"access_token"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshExpiresIn int    `json:"refresh_expires_in"`
	RefreshToken     string `json:"refresh_token"`
	TokenType        string `json:"token_type"`
	Scope            string `json:"scope"`
}

// NewClient creates a new Keycloak client
func NewClient(baseURL, realm, clientID, clientSecret string) *Client {
	return &Client{
		BaseURL:      baseURL,
		Realm:        realm,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		HTTPClient:   &http.Client{Timeout: 30 * time.Second},
	}
}

// AuthenticateClient authenticates the client and gets an access token
func (c *Client) AuthenticateClient() error {
	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	data.Set("client_id", c.ClientID)
	data.Set("client_secret", c.ClientSecret)

	req, err := http.NewRequest("POST", fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", c.BaseURL, c.Realm), bytes.NewBufferString(data.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("failed to close Keycloak response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("authentication failed with status: %d", resp.StatusCode)
	}

	var tokenResp TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return fmt.Errorf("failed to decode token response: %w", err)
	}

	c.AccessToken = tokenResp.AccessToken
	return nil
}

// CreateUser creates a new user in Keycloak
func (c *Client) CreateUser(user *User) error {
	if c.AccessToken == "" {
		if err := c.AuthenticateClient(); err != nil {
			return fmt.Errorf("failed to authenticate client: %w", err)
		}
	}

	userData, err := json.Marshal(user)
	if err != nil {
		return fmt.Errorf("failed to marshal user data: %w", err)
	}

	req, err := http.NewRequest("POST", fmt.Sprintf("%s/admin/realms/%s/users", c.BaseURL, c.Realm), bytes.NewBuffer(userData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("failed to close Keycloak response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("failed to create user with status: %d", resp.StatusCode)
	}

	return nil
}

// GetUser retrieves a user by username
func (c *Client) GetUser(username string) (*User, error) {
	if c.AccessToken == "" {
		if err := c.AuthenticateClient(); err != nil {
			return nil, fmt.Errorf("failed to authenticate client: %w", err)
		}
	}

	req, err := http.NewRequest("GET", fmt.Sprintf("%s/admin/realms/%s/users?username=%s", c.BaseURL, c.Realm, username), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.AccessToken)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("failed to close Keycloak response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get user with status: %d", resp.StatusCode)
	}

	var users []User
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return nil, fmt.Errorf("failed to decode user response: %w", err)
	}

	if len(users) == 0 {
		return nil, fmt.Errorf("user not found: %s", username)
	}

	return &users[0], nil
}

// ListUsers retrieves all users
func (c *Client) ListUsers() ([]User, error) {
	if c.AccessToken == "" {
		if err := c.AuthenticateClient(); err != nil {
			return nil, fmt.Errorf("failed to authenticate client: %w", err)
		}
	}

	req, err := http.NewRequest("GET", fmt.Sprintf("%s/admin/realms/%s/users", c.BaseURL, c.Realm), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.AccessToken)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("failed to close Keycloak response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to list users with status: %d", resp.StatusCode)
	}

	var users []User
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return nil, fmt.Errorf("failed to decode users response: %w", err)
	}

	return users, nil
}

// UpdateUser updates an existing user
func (c *Client) UpdateUser(user *User) error {
	if c.AccessToken == "" {
		if err := c.AuthenticateClient(); err != nil {
			return fmt.Errorf("failed to authenticate client: %w", err)
		}
	}

	if user.ID == "" {
		return fmt.Errorf("user ID is required for update")
	}

	userData, err := json.Marshal(user)
	if err != nil {
		return fmt.Errorf("failed to marshal user data: %w", err)
	}

	req, err := http.NewRequest("PUT", fmt.Sprintf("%s/admin/realms/%s/users/%s", c.BaseURL, c.Realm, user.ID), bytes.NewBuffer(userData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("failed to close Keycloak response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("failed to update user with status: %d", resp.StatusCode)
	}

	return nil
}

// DeleteUser deletes a user
func (c *Client) DeleteUser(userID string) error {
	if c.AccessToken == "" {
		if err := c.AuthenticateClient(); err != nil {
			return fmt.Errorf("failed to authenticate client: %w", err)
		}
	}

	req, err := http.NewRequest("DELETE", fmt.Sprintf("%s/admin/realms/%s/users/%s", c.BaseURL, c.Realm, userID), nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.AccessToken)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("failed to close Keycloak response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("failed to delete user with status: %d", resp.StatusCode)
	}

	return nil
}

// CreateGroup creates a new group
func (c *Client) CreateGroup(group *Group) error {
	if c.AccessToken == "" {
		if err := c.AuthenticateClient(); err != nil {
			return fmt.Errorf("failed to authenticate client: %w", err)
		}
	}

	groupData, err := json.Marshal(group)
	if err != nil {
		return fmt.Errorf("failed to marshal group data: %w", err)
	}

	req, err := http.NewRequest("POST", fmt.Sprintf("%s/admin/realms/%s/groups", c.BaseURL, c.Realm), bytes.NewBuffer(groupData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("failed to close Keycloak response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("failed to create group with status: %d", resp.StatusCode)
	}

	return nil
}

// ListGroups retrieves all groups
func (c *Client) ListGroups() ([]Group, error) {
	if c.AccessToken == "" {
		if err := c.AuthenticateClient(); err != nil {
			return nil, fmt.Errorf("failed to authenticate client: %w", err)
		}
	}

	req, err := http.NewRequest("GET", fmt.Sprintf("%s/admin/realms/%s/groups", c.BaseURL, c.Realm), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.AccessToken)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("failed to close Keycloak response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to list groups with status: %d", resp.StatusCode)
	}

	var groups []Group
	if err := json.NewDecoder(resp.Body).Decode(&groups); err != nil {
		return nil, fmt.Errorf("failed to decode groups response: %w", err)
	}

	return groups, nil
}

// CreateRole creates a new role
func (c *Client) CreateRole(role *Role) error {
	if c.AccessToken == "" {
		if err := c.AuthenticateClient(); err != nil {
			return fmt.Errorf("failed to authenticate client: %w", err)
		}
	}

	roleData, err := json.Marshal(role)
	if err != nil {
		return fmt.Errorf("failed to marshal role data: %w", err)
	}

	req, err := http.NewRequest("POST", fmt.Sprintf("%s/admin/realms/%s/roles", c.BaseURL, c.Realm), bytes.NewBuffer(roleData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("failed to close Keycloak response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("failed to create role with status: %d", resp.StatusCode)
	}

	return nil
}

// ListRoles retrieves all roles
func (c *Client) ListRoles() ([]Role, error) {
	if c.AccessToken == "" {
		if err := c.AuthenticateClient(); err != nil {
			return nil, fmt.Errorf("failed to authenticate client: %w", err)
		}
	}

	req, err := http.NewRequest("GET", fmt.Sprintf("%s/admin/realms/%s/roles", c.BaseURL, c.Realm), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.AccessToken)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("failed to close Keycloak response body: %v", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to list roles with status: %d", resp.StatusCode)
	}

	var roles []Role
	if err := json.NewDecoder(resp.Body).Decode(&roles); err != nil {
		return nil, fmt.Errorf("failed to decode roles response: %w", err)
	}

	return roles, nil
}
