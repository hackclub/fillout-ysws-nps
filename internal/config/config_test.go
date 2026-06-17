package config

import (
	"strings"
	"testing"
	"time"
)

// fullEnv returns a complete, valid environment map for tests to start from.
func fullEnv() map[string]string {
	return map[string]string{
		"HC_AUTH_CLIENT_ID":         "cid",
		"HC_AUTH_CLIENT_SECRET":     "csecret",
		"HC_AUTH_CALLBACK_BASE_URL": "http://localhost:3000",
		"FILLOUT_API_KEY":           "sk_fillout",
		"OPENAI_API_KEY":            "sk_openai",
		"AIRTABLE_API_KEY":          "key_airtable",
		"AIRTABLE_BASE_ID":          "appTest",
		"SESSION_SECRET":            "supersecret",
		"DATABASE_URL":              "postgres://app:app@localhost:5432/app",
		"ALLOWED_EMAILS":            "zach@hackclub.com",
	}
}

func lookupFrom(env map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}
}

func TestLoadFrom_Defaults(t *testing.T) {
	cfg, err := loadFrom(lookupFrom(fullEnv()))
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if cfg.Port != "8080" {
		t.Errorf("Port = %q, want 8080", cfg.Port)
	}
	if cfg.NPSTable != DefaultNPSTable {
		t.Errorf("NPSTable = %q, want %q", cfg.NPSTable, DefaultNPSTable)
	}
	if cfg.PollInterval != DefaultPollInterval {
		t.Errorf("PollInterval = %v, want %v", cfg.PollInterval, DefaultPollInterval)
	}
	if got := cfg.CallbackURL(); got != "http://localhost:3000/callback" {
		t.Errorf("CallbackURL = %q", got)
	}
}

func TestLoadFrom_OverridesAndTrimming(t *testing.T) {
	env := fullEnv()
	env["PORT"] = "9000"
	env["NPS_TABLE"] = "NPS Staging"
	env["NPS_POLL_INTERVAL"] = "5s"
	env["HC_AUTH_CALLBACK_BASE_URL"] = "http://localhost:3000/" // trailing slash
	env["AIRTABLE_BASE_ID"] = "  appPadded  "                   // surrounding space

	cfg, err := loadFrom(lookupFrom(env))
	if err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	if cfg.Port != "9000" {
		t.Errorf("Port = %q", cfg.Port)
	}
	if cfg.NPSTable != "NPS Staging" {
		t.Errorf("NPSTable = %q", cfg.NPSTable)
	}
	if cfg.PollInterval != 5*time.Second {
		t.Errorf("PollInterval = %v", cfg.PollInterval)
	}
	if cfg.CallbackURL() != "http://localhost:3000/callback" {
		t.Errorf("CallbackURL = %q (trailing slash not trimmed)", cfg.CallbackURL())
	}
	if cfg.AirtableBaseID != "appPadded" {
		t.Errorf("AirtableBaseID = %q (not trimmed)", cfg.AirtableBaseID)
	}
}

func TestLoadFrom_MissingRequired(t *testing.T) {
	env := fullEnv()
	delete(env, "FILLOUT_API_KEY")
	delete(env, "ALLOWED_EMAILS")

	_, err := loadFrom(lookupFrom(env))
	if err == nil {
		t.Fatal("expected error for missing required vars, got nil")
	}
	for _, want := range []string{"FILLOUT_API_KEY", "ALLOWED_EMAILS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestLoadFrom_InvalidPollInterval(t *testing.T) {
	for _, raw := range []string{"not-a-duration", "0s", "-3s"} {
		env := fullEnv()
		env["NPS_POLL_INTERVAL"] = raw
		if _, err := loadFrom(lookupFrom(env)); err == nil {
			t.Errorf("NPS_POLL_INTERVAL=%q: expected error, got nil", raw)
		}
	}
}

func TestParseEmails(t *testing.T) {
	got := parseEmails(" Zach@Hackclub.com , admin@hackclub.com,zach@hackclub.com ,, ")
	want := []string{"zach@hackclub.com", "admin@hackclub.com"}
	if len(got) != len(want) {
		t.Fatalf("parseEmails = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("parseEmails[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAllowedEmail(t *testing.T) {
	cfg := &Config{AllowedEmails: []string{"zach@hackclub.com"}}
	cases := map[string]bool{
		"zach@hackclub.com":   true,
		"ZACH@hackclub.com":   true,
		"  zach@hackclub.com": true,
		"nope@hackclub.com":   false,
		"":                    false,
	}
	for email, want := range cases {
		if got := cfg.AllowedEmail(email); got != want {
			t.Errorf("AllowedEmail(%q) = %v, want %v", email, got, want)
		}
	}
}
