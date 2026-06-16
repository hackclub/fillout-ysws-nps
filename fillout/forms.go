package fillout

import (
	"context"
	"net/http"
)

// ListForms returns all forms in the account.
//
// GET /v1/api/forms
func (c *Client) ListForms(ctx context.Context) ([]Form, error) {
	var forms []Form
	if err := c.do(ctx, http.MethodGet, "/forms", nil, nil, &forms); err != nil {
		return nil, err
	}
	return forms, nil
}

// GetForm returns the metadata for a single form, including its questions and
// other field definitions.
//
// GET /v1/api/forms/{formID}
func (c *Client) GetForm(ctx context.Context, formID string) (*FormMetadata, error) {
	var meta FormMetadata
	if err := c.do(ctx, http.MethodGet, "/forms/"+pathEscape(formID), nil, nil, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}
