package airtable

import (
	"encoding/json"
	"fmt"
	"strings"
)

// APIError is returned when the Airtable API responds with a non-2xx status.
type APIError struct {
	// StatusCode is the HTTP status code of the response.
	StatusCode int
	// Type is Airtable's machine-readable error type (e.g. "NOT_FOUND",
	// "INVALID_PERMISSIONS_OR_MODEL_NOT_FOUND").
	Type string
	// Message is the human-readable error message, when one is provided.
	Message string
}

func (e *APIError) Error() string {
	switch {
	case e.Type != "" && e.Message != "":
		return fmt.Sprintf("airtable: HTTP %d: %s: %s", e.StatusCode, e.Type, e.Message)
	case e.Type != "":
		return fmt.Sprintf("airtable: HTTP %d: %s", e.StatusCode, e.Type)
	case e.Message != "":
		return fmt.Sprintf("airtable: HTTP %d: %s", e.StatusCode, e.Message)
	default:
		return fmt.Sprintf("airtable: HTTP %d", e.StatusCode)
	}
}

// parseAPIError builds an *APIError from a response status and body. Airtable
// reports errors in two shapes: an object form
// (`{"error":{"type":"...","message":"..."}}`) used by most endpoints and a
// bare string form (`{"error":"NOT_FOUND"}`) used by some. Both are handled.
func parseAPIError(status int, body []byte) error {
	apiErr := &APIError{StatusCode: status}

	var objForm struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &objForm); err == nil && (objForm.Error.Type != "" || objForm.Error.Message != "") {
		apiErr.Type = objForm.Error.Type
		apiErr.Message = objForm.Error.Message
		return apiErr
	}

	var strForm struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &strForm); err == nil && strForm.Error != "" {
		apiErr.Type = strForm.Error
		return apiErr
	}

	apiErr.Message = strings.TrimSpace(string(body))
	return apiErr
}
