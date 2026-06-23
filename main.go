package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hackclub/fillout-ysws-nps/airtable"
	"github.com/hackclub/fillout-ysws-nps/fillout"
	"github.com/hackclub/fillout-ysws-nps/hcauth"
	"github.com/hackclub/fillout-ysws-nps/internal/auth"
	"github.com/hackclub/fillout-ysws-nps/internal/config"
	"github.com/hackclub/fillout-ysws-nps/internal/db"
	"github.com/hackclub/fillout-ysws-nps/internal/dotenv"
	"github.com/hackclub/fillout-ysws-nps/internal/nps"
	"github.com/hackclub/fillout-ysws-nps/openai"
)

func main() {
	_ = dotenv.Load(".env")

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	oidc, err := hcauth.New(cfg.HCAuthClientID, cfg.HCAuthClientSecret, cfg.CallbackURL())
	if err != nil {
		log.Fatalf("hcauth: %v", err)
	}
	airtableClient, err := airtable.New(cfg.AirtableAPIKey, cfg.AirtableBaseID)
	if err != nil {
		log.Fatalf("airtable: %v", err)
	}
	openaiClient, err := openai.NewClient(cfg.OpenAIAPIKey)
	if err != nil {
		log.Fatalf("openai: %v", err)
	}
	filloutClient := fillout.NewClient(cfg.FilloutAPIKey)

	secureCookies := strings.HasPrefix(cfg.HCAuthCallbackBase, "https://")
	// Login is allowed for ALLOWED_EMAILS plus anyone listed in the YSWS Authors
	// table's Hack Club Auth Email field (read from Airtable, cached).
	allowlist := auth.NewAllowlist(cfg.AllowedEmails, airtableClient)
	authn := auth.New(oidc, cfg.SessionSecret, allowlist.Allowed, secureCookies)
	mapper := nps.NewMapper(openaiClient)
	manager := nps.NewManager(store, filloutClient, airtableClient, cfg.PollInterval, nil)

	if err := manager.Start(ctx); err != nil {
		log.Fatalf("sync manager: %v", err)
	}

	server, err := NewServer(cfg, authn, store, filloutClient, airtableClient, mapper, manager)
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	httpServer := newHTTPServer(":"+cfg.Port, server.Routes())

	go func() {
		<-ctx.Done()
		log.Print("shutting down…")
		manager.Shutdown()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	log.Printf("listening on %s", httpServer.Addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
	log.Print("bye")
}

// newHTTPServer builds the application's http.Server with timeouts that bound
// how long a client may hold a connection, mitigating Slowloris-style attacks
// and leaked idle connections. ReadHeaderTimeout and ReadTimeout cap slow
// request reads; IdleTimeout caps keep-alive sockets. WriteTimeout is generous
// because the preview handler makes a synchronous OpenAI call (its client
// allows up to 60s) before writing the response — it must exceed that.
func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// port returns the configured HTTP port, defaulting to 8080. It is retained for
// the standalone unit tests; main uses config.Config.Port.
func port() string {
	if p := os.Getenv("PORT"); p != "" {
		return p
	}
	return "8080"
}
