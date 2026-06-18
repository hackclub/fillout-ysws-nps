package fillout

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"
)

const oneSubmissionPage = `{
	"responses":[
		{
			"submissionId":"sub1",
			"submissionTime":"2024-01-15T10:30:00Z",
			"lastUpdatedAt":"2024-01-15T10:35:00Z",
			"startedAt":"2024-01-15T10:00:00Z",
			"questions":[
				{"id":"q1","name":"Name","type":"ShortAnswer","value":"Ada"},
				{"id":"q2","name":"Langs","type":"MultiSelect","value":["Go","Rust"]},
				{"id":"q3","name":"Age","type":"NumberInput","value":42},
				{"id":"q4","name":"Subscribe","type":"Switch","value":true}
			],
			"calculations":[{"id":"c1","name":"Score","type":"number","value":"7"}],
			"urlParameters":[{"id":"u1","name":"utm","value":"x"}],
			"quiz":{},
			"documents":[]
		}
	],
	"totalResponses":1,
	"pageCount":1
}`

func TestGetSubmissionsDecodesValues(t *testing.T) {
	c := newTestClient(t, jsonHandler(t, nil, http.StatusOK, oneSubmissionPage))

	page, err := c.GetSubmissions(context.Background(), "form1", nil)
	if err != nil {
		t.Fatalf("GetSubmissions: %v", err)
	}
	if page.TotalResponses != 1 || page.PageCount != 1 {
		t.Errorf("total/pageCount = %d/%d, want 1/1", page.TotalResponses, page.PageCount)
	}
	if len(page.Responses) != 1 {
		t.Fatalf("responses = %d, want 1", len(page.Responses))
	}
	sub := page.Responses[0]
	if sub.SubmissionID != "sub1" {
		t.Errorf("submissionId = %q", sub.SubmissionID)
	}
	wantTime, _ := time.Parse(time.RFC3339, "2024-01-15T10:30:00Z")
	if !sub.SubmissionTime.Equal(wantTime) {
		t.Errorf("submissionTime = %v, want %v", sub.SubmissionTime, wantTime)
	}
	if sub.StartedAt == nil {
		t.Error("startedAt = nil, want set")
	}

	// Value helpers across types.
	if s, err := sub.Questions[0].AsString(); err != nil || s != "Ada" {
		t.Errorf("q1 AsString = %q, %v", s, err)
	}
	if ss, err := sub.Questions[1].AsStringSlice(); err != nil || len(ss) != 2 || ss[0] != "Go" {
		t.Errorf("q2 AsStringSlice = %v, %v", ss, err)
	}
	if f, err := sub.Questions[2].AsFloat(); err != nil || f != 42 {
		t.Errorf("q3 AsFloat = %v, %v", f, err)
	}
	if b, err := sub.Questions[3].AsBool(); err != nil || !b {
		t.Errorf("q4 AsBool = %v, %v", b, err)
	}

	// Decode into a custom target.
	var langs []string
	if err := sub.Questions[1].Decode(&langs); err != nil || len(langs) != 2 {
		t.Errorf("Decode langs = %v, %v", langs, err)
	}

	if len(sub.Calculations) != 1 || sub.Calculations[0].Value != "7" {
		t.Errorf("calculations = %+v", sub.Calculations)
	}
}

func TestGetSubmissionsQueryParams(t *testing.T) {
	var rec recordedRequest
	c := newTestClient(t, jsonHandler(t, &rec, http.StatusOK, `{"responses":[],"totalResponses":0,"pageCount":0}`))

	after := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	before := time.Date(2024, 2, 1, 12, 0, 0, 0, time.UTC)
	params := &GetSubmissionsParams{
		Limit:           25,
		Offset:          50,
		AfterDate:       after,
		BeforeDate:      before,
		Status:          StatusInProgress,
		Sort:            SortDesc,
		IncludeEditLink: true,
		IncludePreview:  true,
		Search:          "hello world",
	}
	if _, err := c.GetSubmissions(context.Background(), "form1", params); err != nil {
		t.Fatalf("GetSubmissions: %v", err)
	}

	q, err := url.ParseQuery(rec.Query)
	if err != nil {
		t.Fatalf("parse query: %v", err)
	}
	checks := map[string]string{
		"limit":           "25",
		"offset":          "50",
		"afterDate":       "2024-01-01T00:00:00Z",
		"beforeDate":      "2024-02-01T12:00:00Z",
		"status":          "in_progress",
		"sort":            "desc",
		"includeEditLink": "true",
		"includePreview":  "true",
		"search":          "hello world",
	}
	for k, want := range checks {
		if got := q.Get(k); got != want {
			t.Errorf("query %s = %q, want %q", k, got, want)
		}
	}
}

func TestGetSubmissionsNilParamsHaveNoQuery(t *testing.T) {
	var rec recordedRequest
	c := newTestClient(t, jsonHandler(t, &rec, http.StatusOK, `{"responses":[],"totalResponses":0,"pageCount":0}`))
	if _, err := c.GetSubmissions(context.Background(), "form1", nil); err != nil {
		t.Fatalf("GetSubmissions: %v", err)
	}
	if rec.Query != "" {
		t.Errorf("query = %q, want empty", rec.Query)
	}
	if rec.Path != "/forms/form1/submissions" {
		t.Errorf("path = %q", rec.Path)
	}
}

func TestGetSubmission(t *testing.T) {
	const body = `{"submission":{"submissionId":"sub9","submissionTime":"2024-01-15T10:30:00Z","lastUpdatedAt":"2024-01-15T10:30:00Z","questions":[],"editLink":"https://edit"}}`
	var rec recordedRequest
	c := newTestClient(t, jsonHandler(t, &rec, http.StatusOK, body))

	sub, err := c.GetSubmission(context.Background(), "form1", "sub9", &GetSubmissionParams{IncludeEditLink: true})
	if err != nil {
		t.Fatalf("GetSubmission: %v", err)
	}
	if rec.Path != "/forms/form1/submissions/sub9" {
		t.Errorf("path = %q", rec.Path)
	}
	if q, _ := url.ParseQuery(rec.Query); q.Get("includeEditLink") != "true" {
		t.Errorf("includeEditLink not set: %q", rec.Query)
	}
	if sub.SubmissionID != "sub9" || sub.EditLink != "https://edit" {
		t.Errorf("submission = %+v", sub)
	}
}

func TestDeleteSubmission(t *testing.T) {
	var rec recordedRequest
	c := newTestClient(t, jsonHandler(t, &rec, http.StatusOK, ``))

	if err := c.DeleteSubmission(context.Background(), "form1", "sub9"); err != nil {
		t.Fatalf("DeleteSubmission: %v", err)
	}
	if rec.Method != http.MethodDelete {
		t.Errorf("method = %s, want DELETE", rec.Method)
	}
	if rec.Path != "/forms/form1/submissions/sub9" {
		t.Errorf("path = %q", rec.Path)
	}
}

func TestCreateSubmissions(t *testing.T) {
	var rec recordedRequest
	resp := `{"submissions":[{"submissionId":"new1","submissionTime":"2024-01-01T12:00:00Z","lastUpdatedAt":"2024-01-01T12:00:00Z","questions":[{"id":"q1","name":"Name","type":"ShortAnswer","value":"Bob"}]}]}`
	c := newTestClient(t, jsonHandler(t, &rec, http.StatusOK, resp))

	in := []SubmissionInput{{
		Questions: []QuestionInput{{ID: "q1", Value: "Bob"}},
		Login:     &Login{Email: "bob@example.com"},
	}}
	out, err := c.CreateSubmissions(context.Background(), "form1", in)
	if err != nil {
		t.Fatalf("CreateSubmissions: %v", err)
	}

	if rec.Method != http.MethodPost || rec.Path != "/forms/form1/submissions" {
		t.Errorf("request = %s %s", rec.Method, rec.Path)
	}
	if ct := rec.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	// Verify the request body round-trips to the documented shape.
	var sent struct {
		Submissions []struct {
			Questions []QuestionInput `json:"questions"`
			Login     *Login          `json:"login"`
		} `json:"submissions"`
	}
	if err := json.Unmarshal([]byte(rec.Body), &sent); err != nil {
		t.Fatalf("unmarshal sent body: %v\nbody=%s", err, rec.Body)
	}
	if len(sent.Submissions) != 1 || sent.Submissions[0].Questions[0].ID != "q1" {
		t.Errorf("sent body = %+v", sent)
	}
	if sent.Submissions[0].Login == nil || sent.Submissions[0].Login.Email != "bob@example.com" {
		t.Errorf("login not sent: %+v", sent.Submissions[0].Login)
	}

	if len(out) != 1 || out[0].SubmissionID != "new1" {
		t.Errorf("response = %+v", out)
	}
}

func TestCreateSubmissionsValidation(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("server should not be called for invalid input")
	})

	if _, err := c.CreateSubmissions(context.Background(), "form1", nil); err == nil {
		t.Error("expected error for empty submissions")
	}

	too := make([]SubmissionInput, maxCreateSubmissions+1)
	if _, err := c.CreateSubmissions(context.Background(), "form1", too); err == nil {
		t.Errorf("expected error for more than %d submissions", maxCreateSubmissions)
	}
}

func TestAllSubmissionsPaginates(t *testing.T) {
	// 3 total submissions, page size 2 -> two pages. The live API reports
	// totalResponses/pageCount PER PAGE (= the number of rows on that page), not
	// as grand totals, so pagination must continue until a page returns fewer
	// rows than the requested limit. The mock mirrors that behavior.
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("limit") != "2" {
			t.Errorf("limit = %q, want 2", q.Get("limit"))
		}
		offset := q.Get("offset")
		w.Header().Set("Content-Type", "application/json")
		switch offset {
		case "", "0":
			// Full page (rows == limit): more may follow.
			w.Write([]byte(`{"responses":[
				{"submissionId":"s1","questions":[]},
				{"submissionId":"s2","questions":[]}
			],"totalResponses":2,"pageCount":1}`))
		case "2":
			// Short page (rows < limit): the end.
			w.Write([]byte(`{"responses":[
				{"submissionId":"s3","questions":[]}
			],"totalResponses":1,"pageCount":1}`))
		default:
			t.Errorf("unexpected offset %q", offset)
			w.Write([]byte(`{"responses":[],"totalResponses":0,"pageCount":0}`))
		}
	})

	var ids []string
	for sub, err := range c.AllSubmissions(context.Background(), "form1", &GetSubmissionsParams{Limit: 2}) {
		if err != nil {
			t.Fatalf("iteration error: %v", err)
		}
		ids = append(ids, sub.SubmissionID)
	}
	want := []string{"s1", "s2", "s3"}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("ids[%d] = %q, want %q", i, ids[i], want[i])
		}
	}
}

func TestAllSubmissionsPagesUntilShortPage(t *testing.T) {
	// When the total is an exact multiple of the page size, every page is full,
	// so iteration only ends on the trailing empty page. This guards against the
	// regression where the per-page totalResponses was used to stop early (which
	// truncated the sync to a single page against the real API).
	var calls int
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("offset") {
		case "", "0":
			w.Write([]byte(`{"responses":[{"submissionId":"a","questions":[]},{"submissionId":"b","questions":[]}],"totalResponses":2,"pageCount":1}`))
		case "2":
			w.Write([]byte(`{"responses":[{"submissionId":"c","questions":[]},{"submissionId":"d","questions":[]}],"totalResponses":2,"pageCount":1}`))
		case "4":
			w.Write([]byte(`{"responses":[],"totalResponses":0,"pageCount":0}`))
		default:
			t.Errorf("unexpected offset %q", r.URL.Query().Get("offset"))
			w.Write([]byte(`{"responses":[],"totalResponses":0,"pageCount":0}`))
		}
	})

	var ids []string
	for sub, err := range c.AllSubmissions(context.Background(), "form1", &GetSubmissionsParams{Limit: 2}) {
		if err != nil {
			t.Fatalf("iteration error: %v", err)
		}
		ids = append(ids, sub.SubmissionID)
	}
	want := []string{"a", "b", "c", "d"}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("ids[%d] = %q, want %q", i, ids[i], want[i])
		}
	}
	if calls != 3 {
		t.Errorf("server calls = %d, want 3 (two full pages + one empty)", calls)
	}
}

func TestAllSubmissionsEarlyBreak(t *testing.T) {
	var calls int
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"responses":[{"submissionId":"s1","questions":[]},{"submissionId":"s2","questions":[]}],"totalResponses":100,"pageCount":50}`))
	})

	var seen int
	for _, err := range c.AllSubmissions(context.Background(), "form1", nil) {
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		seen++
		break
	}
	if seen != 1 {
		t.Errorf("seen = %d, want 1", seen)
	}
	if calls != 1 {
		t.Errorf("server calls = %d, want 1 (must stop after break)", calls)
	}
}

func TestAllSubmissionsYieldsError(t *testing.T) {
	c := newTestClient(t, jsonHandler(t, nil, http.StatusInternalServerError, `{"message":"boom"}`))

	var gotErr error
	var count int
	for sub, err := range c.AllSubmissions(context.Background(), "form1", nil) {
		count++
		if err != nil {
			gotErr = err
			break
		}
		_ = sub
	}
	if gotErr == nil {
		t.Fatal("expected an error to be yielded")
	}
	if !HasStatus(gotErr, http.StatusInternalServerError) {
		t.Errorf("err = %v, want 500 APIError", gotErr)
	}
}
