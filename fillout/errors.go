package fillout

import (
	"errors"
	"fmt"
	"net/http"
)

// APIError is returned when the Fillout API responds with a non-2xx status.
type APIError struct {
	// StatusCode is the HTTP status code returned by the API.
	StatusCode int
	// Message is the human-readable message extracted from the response body,
	// if one was present.
	Message string
	// Body is the raw response body, useful when Message could not be parsed.
	Body []byte
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("fillout: %d %s: %s", e.StatusCode, http.StatusText(e.StatusCode), e.Message)
	}
	if len(e.Body) > 0 {
		return fmt.Sprintf("fillout: %d %s: %s", e.StatusCode, http.StatusText(e.StatusCode), e.Body)
	}
	return fmt.Sprintf("fillout: %d %s", e.StatusCode, http.StatusText(e.StatusCode))
}

// IsNotFound reports whether err is an APIError with a 404 status.
func IsNotFound(err error) bool {
	return HasStatus(err, http.StatusNotFound)
}

// IsRateLimited reports whether err is an APIError with a 429 status.
func IsRateLimited(err error) bool {
	return HasStatus(err, http.StatusTooManyRequests)
}

// IsUnauthorized reports whether err is an APIError with a 401 status.
func IsUnauthorized(err error) bool {
	return HasStatus(err, http.StatusUnauthorized)
}

// HasStatus reports whether err is an APIError carrying the given HTTP status.
func HasStatus(err error, status int) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.StatusCode == status
}
