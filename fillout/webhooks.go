package fillout

import (
	"context"
	"net/http"
)

// CreateWebhook registers a webhook that will receive form submissions at the
// given URL, and returns the new webhook's ID.
//
// POST /v1/api/webhook/create
func (c *Client) CreateWebhook(ctx context.Context, formID, url string) (int, error) {
	body := struct {
		FormID string `json:"formId"`
		URL    string `json:"url"`
	}{FormID: formID, URL: url}
	var resp struct {
		ID int `json:"id"`
	}
	if err := c.do(ctx, http.MethodPost, "/webhook/create", nil, body, &resp); err != nil {
		return 0, err
	}
	return resp.ID, nil
}

// DeleteWebhook removes a previously created webhook by its ID.
//
// POST /v1/api/webhook/delete
func (c *Client) DeleteWebhook(ctx context.Context, webhookID int) error {
	body := struct {
		WebhookID int `json:"webhookId"`
	}{WebhookID: webhookID}
	return c.do(ctx, http.MethodPost, "/webhook/delete", nil, body, nil)
}
