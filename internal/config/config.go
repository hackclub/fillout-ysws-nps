// Package config loads and validates the application's runtime configuration
// from environment variables.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// DefaultPollInterval is how often the sync poller checks Fillout for new
// submissions when NPS_POLL_INTERVAL is unset.
const DefaultPollInterval = 30 * time.Second

// DefaultNPSTable is the Airtable table written to when NPS_TABLE is unset.
const DefaultNPSTable = "NPS"

// minSessionSecretLen is the smallest accepted SESSION_SECRET, in bytes. The
// secret keys the HMAC that protects every session cookie, so a short one
// weakens forgery resistance. 16 bytes (128 bits) is the floor; the .env.example
// recommends 32 random bytes.
const minSessionSecretLen = 16

// Config holds all runtime configuration. Build it with Load.
type Config struct {
	// Port is the HTTP port the server listens on (PORT, default 8080).
	Port string

	// Hack Club Auth (OIDC) credentials for the registered app.
	HCAuthClientID     string
	HCAuthClientSecret string
	// HCAuthCallbackBase is the public base URL of this app; the OAuth callback
	// is HCAuthCallbackBase + "/callback" and must be registered with Hack Club.
	HCAuthCallbackBase string

	// External API keys.
	FilloutAPIKey  string
	OpenAIAPIKey   string
	AirtableAPIKey string
	// AirtableBaseID is the base records are written to. In dev this points at a
	// scratch base; the NPS table is referenced by name so it is portable.
	AirtableBaseID string

	// NPSTable is the Airtable table name for NPS records (default "NPS").
	NPSTable string
	// PollInterval is how often each active sync job polls Fillout.
	PollInterval time.Duration

	// AllowedEmails is the static login allow-list, normalized to lowercase.
	// Authorization also accepts emails listed in the YSWS Authors Airtable table
	// (see auth.Allowlist); this list is the always-available bootstrap.
	AllowedEmails []string
	// SessionSecret is the HMAC key used to sign session cookies.
	SessionSecret []byte

	// DatabaseURL is the Postgres connection string.
	DatabaseURL string
}

// Load reads configuration from the process environment, applying defaults and
// validating that all required values are present.
func Load() (*Config, error) {
	return loadFrom(os.LookupEnv)
}

// loadFrom builds a Config using lookup to read environment variables. It is
// separated from Load so tests can supply a deterministic environment.
func loadFrom(lookup func(string) (string, bool)) (*Config, error) {
	get := func(key string) string {
		v, _ := lookup(key)
		return strings.TrimSpace(v)
	}

	cfg := &Config{
		Port:               firstNonEmpty(get("PORT"), "8080"),
		HCAuthClientID:     get("HC_AUTH_CLIENT_ID"),
		HCAuthClientSecret: get("HC_AUTH_CLIENT_SECRET"),
		HCAuthCallbackBase: strings.TrimRight(get("HC_AUTH_CALLBACK_BASE_URL"), "/"),
		FilloutAPIKey:      get("FILLOUT_API_KEY"),
		OpenAIAPIKey:       get("OPENAI_API_KEY"),
		AirtableAPIKey:     get("AIRTABLE_API_KEY"),
		AirtableBaseID:     get("AIRTABLE_BASE_ID"),
		NPSTable:           firstNonEmpty(get("NPS_TABLE"), DefaultNPSTable),
		AllowedEmails:      parseEmails(get("ALLOWED_EMAILS")),
		SessionSecret:      []byte(get("SESSION_SECRET")),
		DatabaseURL:        get("DATABASE_URL"),
	}

	interval, err := parseInterval(get("NPS_POLL_INTERVAL"))
	if err != nil {
		return nil, err
	}
	cfg.PollInterval = interval

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// CallbackURL returns the full OAuth callback URL registered with Hack Club.
func (c *Config) CallbackURL() string {
	return c.HCAuthCallbackBase + "/callback"
}

func (c *Config) validate() error {
	var missing []string
	required := []struct {
		name string
		val  string
	}{
		{"HC_AUTH_CLIENT_ID", c.HCAuthClientID},
		{"HC_AUTH_CLIENT_SECRET", c.HCAuthClientSecret},
		{"HC_AUTH_CALLBACK_BASE_URL", c.HCAuthCallbackBase},
		{"FILLOUT_API_KEY", c.FilloutAPIKey},
		{"OPENAI_API_KEY", c.OpenAIAPIKey},
		{"AIRTABLE_API_KEY", c.AirtableAPIKey},
		{"AIRTABLE_BASE_ID", c.AirtableBaseID},
		{"SESSION_SECRET", string(c.SessionSecret)},
		{"DATABASE_URL", c.DatabaseURL},
	}
	for _, r := range required {
		if r.val == "" {
			missing = append(missing, r.name)
		}
	}
	if len(c.AllowedEmails) == 0 {
		missing = append(missing, "ALLOWED_EMAILS")
	}
	if len(missing) > 0 {
		return fmt.Errorf("config: missing required environment variables: %s", strings.Join(missing, ", "))
	}
	// SessionSecret is non-empty here (else it was reported missing above); make
	// sure it is long enough to be a usable HMAC key.
	if n := len(c.SessionSecret); n < minSessionSecretLen {
		return fmt.Errorf("config: SESSION_SECRET must be at least %d bytes, got %d; generate one with: head -c 32 /dev/urandom | base64", minSessionSecretLen, n)
	}
	return nil
}

func parseInterval(raw string) (time.Duration, error) {
	if raw == "" {
		return DefaultPollInterval, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("config: invalid NPS_POLL_INTERVAL %q: %w", raw, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("config: NPS_POLL_INTERVAL must be positive, got %q", raw)
	}
	return d, nil
}

// parseEmails splits a comma-separated list into a normalized, de-duplicated
// slice of lowercase addresses.
func parseEmails(raw string) []string {
	if raw == "" {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	for _, part := range strings.Split(raw, ",") {
		email := strings.ToLower(strings.TrimSpace(part))
		if email == "" || seen[email] {
			continue
		}
		seen[email] = true
		out = append(out, email)
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
