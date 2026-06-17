package hcauth

import (
	"encoding/json"
	"fmt"
	"strings"
)

// OAuthError is an RFC 6749 error response from the authorization server, such
// as {"error":"invalid_grant"} returned by the token endpoint.
type OAuthError struct {
	StatusCode  int    `json:"-"`
	Code        string `json:"error"`
	Description string `json:"error_description"`
	URI         string `json:"error_uri"`
}

func (e *OAuthError) Error() string {
	msg := e.Code
	if e.Description != "" {
		msg += ": " + e.Description
	}
	return fmt.Sprintf("hcauth: oauth error (status %d): %s", e.StatusCode, msg)
}

// APIError is returned for a non-2xx response that is not a structured OAuth
// error (for example an HTML error page or an empty body).
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("hcauth: server returned status %d: %s", e.StatusCode, e.Body)
}

// responseError converts a non-2xx response body into the most specific error
// type available: an *OAuthError when the body is a JSON error object,
// otherwise an *APIError.
func responseError(statusCode int, body []byte) error {
	var oauthErr OAuthError
	if json.Unmarshal(body, &oauthErr) == nil && oauthErr.Code != "" {
		oauthErr.StatusCode = statusCode
		return &oauthErr
	}
	return &APIError{StatusCode: statusCode, Body: strings.TrimSpace(string(body))}
}
