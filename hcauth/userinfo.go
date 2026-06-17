package hcauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// UserInfo is the profile returned by the user-info endpoint. Which fields are
// populated depends on the granted scopes. Claims not modeled here are still
// available in [UserInfo.Raw].
type UserInfo struct {
	// Subject is the stable, unique identifier for the user (the "sub" claim).
	// Use it as the primary key for an account; it never changes.
	Subject       string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified Bool   `json:"email_verified"`
	Name          string `json:"name"`
	GivenName     string `json:"given_name"`
	FamilyName    string `json:"family_name"`
	Nickname      string `json:"nickname"`

	// Hack Club-specific claims.
	SlackID            string `json:"slack_id"`
	VerificationStatus string `json:"verification_status"`
	YSWSEligible       Bool   `json:"ysws_eligible"`

	// Raw holds every claim from the response, including any not represented by
	// a typed field above.
	Raw map[string]json.RawMessage `json:"-"`
}

// UserInfo fetches the authenticated user's profile from the user-info
// endpoint using the access token as a bearer credential.
func (c *Client) UserInfo(ctx context.Context, accessToken string) (*UserInfo, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, fmt.Errorf("hcauth: access token is required")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.userInfoURL, nil)
	if err != nil {
		return nil, fmt.Errorf("hcauth: building userinfo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hcauth: sending userinfo request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("hcauth: reading userinfo response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, responseError(resp.StatusCode, body)
	}

	var info UserInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("hcauth: decoding userinfo response: %w", err)
	}
	if err := json.Unmarshal(body, &info.Raw); err != nil {
		return nil, fmt.Errorf("hcauth: decoding userinfo claims: %w", err)
	}
	if info.Subject == "" {
		return nil, fmt.Errorf("hcauth: userinfo response missing sub")
	}
	return &info, nil
}

// Bool is a boolean that also accepts a JSON string ("true"/"false"). Some
// OIDC providers serialize claims such as email_verified as quoted strings;
// this keeps decoding robust either way.
type Bool bool

// UnmarshalJSON implements [json.Unmarshaler].
func (b *Bool) UnmarshalJSON(data []byte) error {
	switch s := strings.Trim(string(bytes.TrimSpace(data)), `"`); s {
	case "true":
		*b = true
	case "false", "", "null":
		*b = false
	default:
		return fmt.Errorf("hcauth: cannot parse %q as bool", s)
	}
	return nil
}
