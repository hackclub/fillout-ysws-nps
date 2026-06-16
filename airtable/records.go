package airtable

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Record is a single Airtable record. On reads, ID and CreatedTime are
// populated by the API. On writes, only Fields (and ID, for updates) are sent.
type Record struct {
	ID          string         `json:"id,omitempty"`
	CreatedTime time.Time      `json:"createdTime,omitempty"`
	Fields      map[string]any `json:"fields"`
}

// SortSpec describes a single sort directive for ListRecords.
type SortSpec struct {
	// Field is the name of the field to sort by.
	Field string
	// Direction is "asc" (the default when empty) or "desc".
	Direction string
}

// ListOptions controls which records ListRecords and ListRecordsPage return.
// The zero value lists every record using Airtable's defaults.
type ListOptions struct {
	// View limits results to a named view and applies its filters and sort.
	View string
	// Fields restricts the returned fields to those named.
	Fields []string
	// FilterByFormula is an Airtable formula used to filter records.
	FilterByFormula string
	// Sort orders the records; multiple specs are applied in order.
	Sort []SortSpec
	// MaxRecords caps the total number of records returned across all pages.
	// Zero means no cap.
	MaxRecords int
	// PageSize sets the records per page (Airtable allows up to 100). Zero
	// uses Airtable's default.
	PageSize int
	// Offset starts pagination from a specific page token. It is normally
	// left empty and managed automatically.
	Offset string
}

func (o *ListOptions) values() url.Values {
	v := url.Values{}
	if o == nil {
		return v
	}
	if o.View != "" {
		v.Set("view", o.View)
	}
	for _, f := range o.Fields {
		v.Add("fields[]", f)
	}
	if o.FilterByFormula != "" {
		v.Set("filterByFormula", o.FilterByFormula)
	}
	for i, s := range o.Sort {
		v.Set(fmt.Sprintf("sort[%d][field]", i), s.Field)
		if s.Direction != "" {
			v.Set(fmt.Sprintf("sort[%d][direction]", i), s.Direction)
		}
	}
	if o.MaxRecords > 0 {
		v.Set("maxRecords", strconv.Itoa(o.MaxRecords))
	}
	if o.PageSize > 0 {
		v.Set("pageSize", strconv.Itoa(o.PageSize))
	}
	if o.Offset != "" {
		v.Set("offset", o.Offset)
	}
	return v
}

type listResponse struct {
	Records []Record `json:"records"`
	Offset  string   `json:"offset"`
}

// ListRecordsPage returns a single page of records along with the offset token
// for the next page. An empty offset means there are no more pages. Use it when
// you need manual control over pagination; otherwise prefer ListRecords.
func (c *Client) ListRecordsPage(ctx context.Context, table string, opts *ListOptions) (records []Record, nextOffset string, err error) {
	var resp listResponse
	if err := c.do(ctx, http.MethodGet, c.tablePath(table), opts.values(), nil, &resp); err != nil {
		return nil, "", err
	}
	return resp.Records, resp.Offset, nil
}

// ListRecords returns all records in a table, transparently following
// pagination. If opts.MaxRecords is set, no more than that many records are
// returned.
func (c *Client) ListRecords(ctx context.Context, table string, opts *ListOptions) ([]Record, error) {
	var o ListOptions
	if opts != nil {
		o = *opts
	}

	var all []Record
	for {
		var resp listResponse
		if err := c.do(ctx, http.MethodGet, c.tablePath(table), o.values(), nil, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Records...)

		if o.MaxRecords > 0 && len(all) >= o.MaxRecords {
			all = all[:o.MaxRecords]
			break
		}
		if resp.Offset == "" {
			break
		}
		o.Offset = resp.Offset
	}
	return all, nil
}

// GetRecord retrieves a single record by ID.
func (c *Client) GetRecord(ctx context.Context, table, recordID string) (*Record, error) {
	var r Record
	if err := c.do(ctx, http.MethodGet, c.recordPath(table, recordID), nil, nil, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

type writeRecord struct {
	ID     string         `json:"id,omitempty"`
	Fields map[string]any `json:"fields"`
}

type writeRequest struct {
	Records  []writeRecord `json:"records"`
	Typecast bool          `json:"typecast,omitempty"`
}

// CreateRecords creates one record per supplied field map. Inputs are split
// into batches of 10 (Airtable's per-request limit) and the created records,
// including their assigned IDs, are returned in order. When typecast is true,
// Airtable coerces string values into the destination field's type.
func (c *Client) CreateRecords(ctx context.Context, table string, fields []map[string]any, typecast bool) ([]Record, error) {
	if len(fields) == 0 {
		return nil, nil
	}

	created := make([]Record, 0, len(fields))
	for _, batch := range chunk(fields, maxBatchSize) {
		records := make([]writeRecord, len(batch))
		for i, f := range batch {
			records[i] = writeRecord{Fields: f}
		}
		var resp listResponse
		if err := c.do(ctx, http.MethodPost, c.tablePath(table), nil, writeRequest{Records: records, Typecast: typecast}, &resp); err != nil {
			return created, err
		}
		created = append(created, resp.Records...)
	}
	return created, nil
}

// CreateRecord creates a single record and returns it.
func (c *Client) CreateRecord(ctx context.Context, table string, fields map[string]any, typecast bool) (*Record, error) {
	created, err := c.CreateRecords(ctx, table, []map[string]any{fields}, typecast)
	if err != nil {
		return nil, err
	}
	if len(created) == 0 {
		return nil, errors.New("airtable: create returned no record")
	}
	return &created[0], nil
}

// UpdateRecords performs a partial (PATCH) update: only the fields present in
// each record are changed; other fields are left untouched. Every record must
// have a non-empty ID. Inputs are batched in groups of 10.
func (c *Client) UpdateRecords(ctx context.Context, table string, records []Record, typecast bool) ([]Record, error) {
	return c.writeRecords(ctx, http.MethodPatch, table, records, typecast)
}

// ReplaceRecords performs a destructive (PUT) update: any field not present in
// a record is cleared. Every record must have a non-empty ID. Inputs are
// batched in groups of 10.
func (c *Client) ReplaceRecords(ctx context.Context, table string, records []Record, typecast bool) ([]Record, error) {
	return c.writeRecords(ctx, http.MethodPut, table, records, typecast)
}

func (c *Client) writeRecords(ctx context.Context, method, table string, records []Record, typecast bool) ([]Record, error) {
	if len(records) == 0 {
		return nil, nil
	}

	updated := make([]Record, 0, len(records))
	for _, batch := range chunk(records, maxBatchSize) {
		payload := make([]writeRecord, len(batch))
		for i, r := range batch {
			if r.ID == "" {
				return updated, errors.New("airtable: every record to update must have an ID")
			}
			payload[i] = writeRecord{ID: r.ID, Fields: r.Fields}
		}
		var resp listResponse
		if err := c.do(ctx, method, c.tablePath(table), nil, writeRequest{Records: payload, Typecast: typecast}, &resp); err != nil {
			return updated, err
		}
		updated = append(updated, resp.Records...)
	}
	return updated, nil
}

// UpdateRecord partially updates a single record by ID and returns it.
func (c *Client) UpdateRecord(ctx context.Context, table, recordID string, fields map[string]any, typecast bool) (*Record, error) {
	updated, err := c.UpdateRecords(ctx, table, []Record{{ID: recordID, Fields: fields}}, typecast)
	if err != nil {
		return nil, err
	}
	if len(updated) == 0 {
		return nil, errors.New("airtable: update returned no record")
	}
	return &updated[0], nil
}

type deleteResponse struct {
	Records []struct {
		ID      string `json:"id"`
		Deleted bool   `json:"deleted"`
	} `json:"records"`
}

// DeleteRecords deletes records by ID, batching in groups of 10, and returns
// the IDs that Airtable confirmed as deleted.
func (c *Client) DeleteRecords(ctx context.Context, table string, recordIDs []string) ([]string, error) {
	if len(recordIDs) == 0 {
		return nil, nil
	}

	deleted := make([]string, 0, len(recordIDs))
	for _, batch := range chunk(recordIDs, maxBatchSize) {
		q := url.Values{}
		for _, id := range batch {
			q.Add("records[]", id)
		}
		var resp deleteResponse
		if err := c.do(ctx, http.MethodDelete, c.tablePath(table), q, nil, &resp); err != nil {
			return deleted, err
		}
		for _, r := range resp.Records {
			if r.Deleted {
				deleted = append(deleted, r.ID)
			}
		}
	}
	return deleted, nil
}

// DeleteRecord deletes a single record by ID.
func (c *Client) DeleteRecord(ctx context.Context, table, recordID string) error {
	var resp struct {
		ID      string `json:"id"`
		Deleted bool   `json:"deleted"`
	}
	if err := c.do(ctx, http.MethodDelete, c.recordPath(table, recordID), nil, nil, &resp); err != nil {
		return err
	}
	if !resp.Deleted {
		return fmt.Errorf("airtable: record %q was not deleted", recordID)
	}
	return nil
}
