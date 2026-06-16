package fillout

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestCreateWebhook(t *testing.T) {
	var rec recordedRequest
	c := newTestClient(t, jsonHandler(t, &rec, http.StatusOK, `{"id":42}`))

	id, err := c.CreateWebhook(context.Background(), "form1", "https://example.com/hook")
	if err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}
	if id != 42 {
		t.Errorf("id = %d, want 42", id)
	}
	if rec.Method != http.MethodPost || rec.Path != "/webhook/create" {
		t.Errorf("request = %s %s", rec.Method, rec.Path)
	}

	var sent struct {
		FormID string `json:"formId"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal([]byte(rec.Body), &sent); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if sent.FormID != "form1" || sent.URL != "https://example.com/hook" {
		t.Errorf("sent = %+v", sent)
	}
}

func TestDeleteWebhook(t *testing.T) {
	var rec recordedRequest
	c := newTestClient(t, jsonHandler(t, &rec, http.StatusOK, `{}`))

	if err := c.DeleteWebhook(context.Background(), 42); err != nil {
		t.Fatalf("DeleteWebhook: %v", err)
	}
	if rec.Method != http.MethodPost || rec.Path != "/webhook/delete" {
		t.Errorf("request = %s %s", rec.Method, rec.Path)
	}
	var sent struct {
		WebhookID int `json:"webhookId"`
	}
	if err := json.Unmarshal([]byte(rec.Body), &sent); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if sent.WebhookID != 42 {
		t.Errorf("webhookId = %d, want 42", sent.WebhookID)
	}
}
