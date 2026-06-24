package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// isoTime must emit a machine-readable, absolute-instant RFC3339 value in UTC so
// the browser can localize it. The wall-clock zone of the input must not change
// the instant it represents.
func TestISOTime(t *testing.T) {
	if got := isoTime(nil); got != "" {
		t.Errorf("isoTime(nil) = %q, want empty", got)
	}

	// Same instant expressed in a non-UTC zone must serialize to its UTC form.
	loc := time.FixedZone("PST", -8*3600)
	at := time.Date(2026, 6, 24, 7, 30, 0, 0, loc) // == 15:30 UTC
	if got, want := isoTime(&at), "2026-06-24T15:30:00Z"; got != want {
		t.Errorf("isoTime(%v) = %q, want %q", at, got, want)
	}
}

// formatTime is the no-JS fallback. It must be unambiguous about its zone so a
// viewer without JavaScript is not misled into reading server time as local.
func TestFormatTimeIsExplicitUTC(t *testing.T) {
	if got := formatTime(nil); got != "never" {
		t.Errorf("formatTime(nil) = %q, want %q", got, "never")
	}

	loc := time.FixedZone("PST", -8*3600)
	at := time.Date(2026, 6, 24, 7, 30, 0, 0, loc) // == 15:30 UTC
	got := formatTime(&at)
	if !strings.Contains(got, "UTC") {
		t.Errorf("formatTime(%v) = %q, want it to name the UTC zone", at, got)
	}
	if !strings.Contains(got, "15:30") {
		t.Errorf("formatTime(%v) = %q, want UTC wall clock 15:30", at, got)
	}
}

// The dashboard must render the last-poll timestamp as a <time> element carrying
// the absolute instant, tagged for client-side localization, with the UTC text
// as the visible fallback. The localization script must ship with the page.
func TestDashboardLocalizesLastPoll(t *testing.T) {
	s := newTestServer(t, &fakeForms{}, nil)

	at := time.Date(2026, 6, 24, 15, 30, 0, 0, time.UTC)
	view := dashboardView{
		baseView: baseView{Title: "Dashboard", UserEmail: "z@hackclub.com"},
		Jobs: []jobRow{{
			ID: 1, FormName: "Demo", FormID: "f1", BaseID: "appTest", Table: "NPS",
			CreatedBy: "z@hackclub.com", Status: "Active", Running: true,
			LastPolled: formatTime(&at), LastPolledISO: isoTime(&at),
		}},
	}

	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, "page_dashboard", view); err != nil {
		t.Fatalf("ExecuteTemplate: %v", err)
	}
	html := buf.String()

	if !strings.Contains(html, `datetime="2026-06-24T15:30:00Z"`) {
		t.Errorf("dashboard missing machine-readable datetime attribute:\n%s", html)
	}
	if !strings.Contains(html, "data-localtime") {
		t.Errorf("dashboard missing data-localtime hook for client localization:\n%s", html)
	}
	if !strings.Contains(html, formatTime(&at)) {
		t.Errorf("dashboard missing UTC fallback text %q:\n%s", formatTime(&at), html)
	}
	// The localization script must be present on the page.
	if !strings.Contains(html, "data-localtime") || !strings.Contains(html, "toLocaleString") {
		t.Errorf("dashboard missing client-side localization script:\n%s", html)
	}
}

// A job that has never polled must not emit a <time> element — there is no
// instant to localize, only the literal "never".
func TestDashboardNeverPolledHasNoTimeElement(t *testing.T) {
	s := newTestServer(t, &fakeForms{}, nil)

	view := dashboardView{
		baseView: baseView{Title: "Dashboard", UserEmail: "z@hackclub.com"},
		Jobs: []jobRow{{
			ID: 1, FormName: "Demo", FormID: "f1", BaseID: "appTest", Table: "NPS",
			CreatedBy: "z@hackclub.com", Status: "Active",
			LastPolled: formatTime(nil), LastPolledISO: isoTime(nil),
		}},
	}

	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, "page_dashboard", view); err != nil {
		t.Fatalf("ExecuteTemplate: %v", err)
	}
	html := buf.String()

	if !strings.Contains(html, "Last poll never") {
		t.Errorf("dashboard should show 'Last poll never' for an unpolled job:\n%s", html)
	}
	if strings.Contains(html, "<time") {
		t.Errorf("dashboard should not emit a <time> element when never polled:\n%s", html)
	}
}
