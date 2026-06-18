package fillout

import (
	"context"
	"fmt"
	"iter"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// SubmissionStatus filters submissions by completion state.
type SubmissionStatus string

const (
	// StatusFinished selects completed submissions (the API default).
	StatusFinished SubmissionStatus = "finished"
	// StatusInProgress selects unfinished submissions.
	StatusInProgress SubmissionStatus = "in_progress"
)

// SortOrder controls the ordering of listed submissions.
type SortOrder string

const (
	SortAsc  SortOrder = "asc"
	SortDesc SortOrder = "desc"
)

// maxPageLimit is the largest page size the API accepts for GetSubmissions.
const maxPageLimit = 150

// GetSubmissionsParams holds the optional query parameters for GetSubmissions.
// The zero value requests the API defaults (50 finished submissions, ascending).
type GetSubmissionsParams struct {
	// Limit is the maximum number of submissions to return per page (1-150).
	// Zero means use the API default of 50.
	Limit int
	// Offset is the starting position to fetch from.
	Offset int
	// AfterDate, if non-zero, returns only submissions after this time.
	AfterDate time.Time
	// BeforeDate, if non-zero, returns only submissions before this time.
	BeforeDate time.Time
	// Status filters by completion state. Empty means finished.
	Status SubmissionStatus
	// Sort controls ordering. Empty means ascending.
	Sort SortOrder
	// IncludeEditLink populates Submission.EditLink in the response.
	IncludeEditLink bool
	// IncludePreview includes preview (test) responses.
	IncludePreview bool
	// Search filters to submissions containing this text.
	Search string
}

func (p *GetSubmissionsParams) values() url.Values {
	q := url.Values{}
	if p == nil {
		return q
	}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	if p.Offset > 0 {
		q.Set("offset", strconv.Itoa(p.Offset))
	}
	if !p.AfterDate.IsZero() {
		q.Set("afterDate", p.AfterDate.UTC().Format(time.RFC3339))
	}
	if !p.BeforeDate.IsZero() {
		q.Set("beforeDate", p.BeforeDate.UTC().Format(time.RFC3339))
	}
	if p.Status != "" {
		q.Set("status", string(p.Status))
	}
	if p.Sort != "" {
		q.Set("sort", string(p.Sort))
	}
	if p.IncludeEditLink {
		q.Set("includeEditLink", "true")
	}
	if p.IncludePreview {
		q.Set("includePreview", "true")
	}
	if p.Search != "" {
		q.Set("search", p.Search)
	}
	return q
}

// SubmissionsPage is one page of results from GetSubmissions.
type SubmissionsPage struct {
	// Responses are the submissions on this page.
	Responses []Submission `json:"responses"`
	// TotalResponses is, despite its name, reported by the live API as the number
	// of responses on THIS page (i.e. min(limit, remaining)), not the grand total
	// across all pages. Do not use it to compute totals or to drive pagination;
	// page until a page returns fewer rows than the requested limit (AllSubmissions
	// does this).
	TotalResponses int `json:"totalResponses"`
	// PageCount is likewise reported per-page (typically 1) rather than as the
	// total number of pages, and is unreliable for the same reason.
	PageCount int `json:"pageCount"`
}

// GetSubmissions returns one page of submissions for a form. Pass nil params
// for the API defaults. To iterate every submission, use AllSubmissions.
//
// GET /v1/api/forms/{formID}/submissions
func (c *Client) GetSubmissions(ctx context.Context, formID string, params *GetSubmissionsParams) (*SubmissionsPage, error) {
	var page SubmissionsPage
	path := "/forms/" + pathEscape(formID) + "/submissions"
	if err := c.do(ctx, http.MethodGet, path, params.values(), nil, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

// GetSubmissionParams holds the optional query parameters for GetSubmission.
type GetSubmissionParams struct {
	// IncludeEditLink populates Submission.EditLink in the response.
	IncludeEditLink bool
}

func (p *GetSubmissionParams) values() url.Values {
	q := url.Values{}
	if p != nil && p.IncludeEditLink {
		q.Set("includeEditLink", "true")
	}
	return q
}

// GetSubmission returns a single submission by ID. Pass nil params for defaults.
//
// GET /v1/api/forms/{formID}/submissions/{submissionID}
func (c *Client) GetSubmission(ctx context.Context, formID, submissionID string, params *GetSubmissionParams) (*Submission, error) {
	var wrapper struct {
		Submission Submission `json:"submission"`
	}
	path := "/forms/" + pathEscape(formID) + "/submissions/" + pathEscape(submissionID)
	if err := c.do(ctx, http.MethodGet, path, params.values(), nil, &wrapper); err != nil {
		return nil, err
	}
	return &wrapper.Submission, nil
}

// DeleteSubmission permanently deletes a submission by ID.
//
// DELETE /v1/api/forms/{formID}/submissions/{submissionID}
func (c *Client) DeleteSubmission(ctx context.Context, formID, submissionID string) error {
	path := "/forms/" + pathEscape(formID) + "/submissions/" + pathEscape(submissionID)
	return c.do(ctx, http.MethodDelete, path, nil, nil, nil)
}

// maxCreateSubmissions is the most submissions the API accepts per create call.
const maxCreateSubmissions = 10

// CreateSubmissions creates up to 10 submissions for a form and returns the
// created submissions. Note that submissions created via the API do not trigger
// email notifications, workflows, or integrations.
//
// POST /v1/api/forms/{formID}/submissions
func (c *Client) CreateSubmissions(ctx context.Context, formID string, submissions []SubmissionInput) ([]Submission, error) {
	if len(submissions) == 0 {
		return nil, fmt.Errorf("fillout: CreateSubmissions requires at least one submission")
	}
	if len(submissions) > maxCreateSubmissions {
		return nil, fmt.Errorf("fillout: CreateSubmissions accepts at most %d submissions, got %d", maxCreateSubmissions, len(submissions))
	}
	body := struct {
		Submissions []SubmissionInput `json:"submissions"`
	}{Submissions: submissions}
	var resp struct {
		Submissions []Submission `json:"submissions"`
	}
	path := "/forms/" + pathEscape(formID) + "/submissions"
	if err := c.do(ctx, http.MethodPost, path, nil, body, &resp); err != nil {
		return nil, err
	}
	return resp.Submissions, nil
}

// AllSubmissions returns an iterator over every submission matching params,
// transparently paging through the API. params.Offset sets the starting point;
// params.Limit caps the page size (defaulting to the API maximum of 150 to
// minimize round trips). Iteration stops on the first error, which is yielded
// alongside a zero Submission. Break out of the range loop to stop early.
//
//	for sub, err := range client.AllSubmissions(ctx, formID, nil) {
//		if err != nil {
//			return err
//		}
//		// use sub
//	}
func (c *Client) AllSubmissions(ctx context.Context, formID string, params *GetSubmissionsParams) iter.Seq2[Submission, error] {
	// Copy params so callers' structs are not mutated and so we can advance
	// the offset across pages.
	var p GetSubmissionsParams
	if params != nil {
		p = *params
	}
	if p.Limit <= 0 {
		p.Limit = maxPageLimit
	}

	return func(yield func(Submission, error) bool) {
		for {
			page, err := c.GetSubmissions(ctx, formID, &p)
			if err != nil {
				yield(Submission{}, err)
				return
			}
			for _, sub := range page.Responses {
				if !yield(sub, nil) {
					return
				}
			}
			// Stop on a short page. The API reports totalResponses/pageCount
			// per-page (= rows on this page), not as grand totals, so they cannot
			// drive pagination; a page smaller than the requested limit means we
			// have reached the end. When the total is an exact multiple of the
			// limit this costs one extra empty request, which also terminates here.
			if len(page.Responses) < p.Limit {
				return
			}
			p.Offset += len(page.Responses)
		}
	}
}
