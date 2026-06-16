package fillout

import (
	"context"
	"net/http"
	"testing"
)

func TestListForms(t *testing.T) {
	const body = `[
		{"id":101,"formId":"abc123","name":"Contact Us","tags":["sales","web"],"isPublished":true},
		{"id":102,"formId":"xyz789","name":"Draft","tags":[],"isPublished":false}
	]`
	var rec recordedRequest
	c := newTestClient(t, jsonHandler(t, &rec, http.StatusOK, body))

	forms, err := c.ListForms(context.Background())
	if err != nil {
		t.Fatalf("ListForms: %v", err)
	}
	if rec.Method != http.MethodGet || rec.Path != "/forms" {
		t.Errorf("request = %s %s, want GET /forms", rec.Method, rec.Path)
	}
	if len(forms) != 2 {
		t.Fatalf("got %d forms, want 2", len(forms))
	}
	want := Form{ID: 101, FormID: "abc123", Name: "Contact Us", Tags: []string{"sales", "web"}, IsPublished: true}
	got := forms[0]
	if got.ID != want.ID || got.FormID != want.FormID || got.Name != want.Name || got.IsPublished != want.IsPublished {
		t.Errorf("forms[0] = %+v, want %+v", got, want)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "sales" || got.Tags[1] != "web" {
		t.Errorf("forms[0].Tags = %v, want [sales web]", got.Tags)
	}
	if forms[1].IsPublished {
		t.Error("forms[1].IsPublished = true, want false")
	}
}

func TestGetForm(t *testing.T) {
	const body = `{
		"id":"abc123",
		"name":"Survey",
		"tags":["nps"],
		"questions":[
			{"id":"q1","name":"Your name","type":"ShortAnswer"},
			{"id":"q2","name":"Pick options","type":"MultiSelect"}
		],
		"calculations":[{"id":"c1","name":"Total","type":"number"}],
		"urlParameters":[{"id":"u1","name":"utm_source"}],
		"scheduling":[],
		"payments":[],
		"documents":[]
	}`
	var rec recordedRequest
	c := newTestClient(t, jsonHandler(t, &rec, http.StatusOK, body))

	meta, err := c.GetForm(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("GetForm: %v", err)
	}
	if rec.Path != "/forms/abc123" {
		t.Errorf("path = %q, want /forms/abc123", rec.Path)
	}
	if meta.ID != "abc123" || meta.Name != "Survey" {
		t.Errorf("meta id/name = %q/%q", meta.ID, meta.Name)
	}
	if len(meta.Questions) != 2 {
		t.Fatalf("questions = %d, want 2", len(meta.Questions))
	}
	if meta.Questions[1].Type != QuestionMultiSelect {
		t.Errorf("questions[1].Type = %q, want MultiSelect", meta.Questions[1].Type)
	}
	if len(meta.Calculations) != 1 || meta.Calculations[0].Type != CalculationNumber {
		t.Errorf("calculations = %+v", meta.Calculations)
	}
	if len(meta.URLParameters) != 1 || meta.URLParameters[0].Name != "utm_source" {
		t.Errorf("urlParameters = %+v", meta.URLParameters)
	}
}

func TestGetFormEscapesID(t *testing.T) {
	var rec recordedRequest
	c := newTestClient(t, jsonHandler(t, &rec, http.StatusOK, `{"id":"x","name":"x","questions":[]}`))

	// A form ID containing a space and a slash must be percent-escaped so it
	// stays a single path segment and produces a valid request.
	if _, err := c.GetForm(context.Background(), "a b/c"); err != nil {
		t.Fatalf("GetForm: %v", err)
	}
	if rec.Path != "/forms/a b/c" {
		t.Errorf("decoded path = %q, want /forms/a b/c", rec.Path)
	}
}
