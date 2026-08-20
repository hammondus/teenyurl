package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// config is read from the environment rather than flags: this service only
// ever runs in Compose, where environment: and env_file: are the natural home
// for a password. Supporting both would mean two places to look when a setting
// appears wrong.
type config struct {
	addr           string
	baseURL        string
	dataDir        string
	trustedProxies string
	flushInterval  time.Duration
	codeLen        int
}

// host is the display name of the service, for the landing page.
func (c config) host() string {
	u, err := url.Parse(c.baseURL)
	if err != nil || u.Host == "" {
		return c.baseURL
	}
	return u.Host
}

func loadConfig() (config, error) {
	c := config{
		addr:           envOr("TEENYURL_ADDR", ":8080"),
		baseURL:        strings.TrimRight(envOr("TEENYURL_BASE_URL", "http://localhost:8080"), "/"),
		dataDir:        envOr("TEENYURL_DATA_DIR", "data"),
		trustedProxies: envOr("TEENYURL_TRUSTED_PROXIES", "127.0.0.1/32,::1/128"),
	}
	var err error
	if c.flushInterval, err = envDuration("TEENYURL_FLUSH_INTERVAL", 30*time.Second); err != nil {
		return c, err
	}
	if c.codeLen, err = envInt("TEENYURL_CODE_LEN", defaultCodeLen); err != nil {
		return c, err
	}
	if c.codeLen < 1 || c.codeLen > 64 {
		return c, fmt.Errorf("TEENYURL_CODE_LEN must be between 1 and 64, got %d", c.codeLen)
	}
	if u, err := url.Parse(c.baseURL); err != nil || u.Host == "" {
		return c, fmt.Errorf("TEENYURL_BASE_URL %q is not an absolute URL", c.baseURL)
	}
	return c, nil
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be positive, got %s", key, v)
	}
	return d, nil
}

func envInt(key string, fallback int) (int, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	if err := run(); err != nil {
		log.Fatalf("teenyurl: %v", err)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	trust, err := parseTrustedProxies(cfg.trustedProxies)
	if err != nil {
		return err
	}
	rn, err := newRenderer()
	if err != nil {
		return err
	}
	store, err := OpenStore(cfg.dataDir, cfg.codeLen)
	if err != nil {
		return err
	}
	defer store.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go store.FlushLoop(ctx, cfg.flushInterval)

	srv := &http.Server{
		Addr:    cfg.addr,
		Handler: newServer(cfg, store, rn, trust).routes(),
		// Timeouts are set because the listener faces a reverse proxy, not
		// only trusted callers. Without them a stalled connection holds a
		// goroutine and a file descriptor indefinitely.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          log.Default(),
	}

	errc := make(chan error, 1)
	go func() {
		log.Printf("teenyurl: listening on %s for %s", cfg.addr, cfg.baseURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	log.Print("teenyurl: shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	// Close flushes the click counts that the flush loop has not written yet.
	return store.Close()
}
