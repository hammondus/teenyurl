package main

import (
	"net/http"
	"net/url"
	"strings"
	"time"
)

// server holds everything the handlers need.
type server struct {
	cfg   config
	store *Store
	rn    *renderer
	trust *proxyTrust
	// now is a field so that tests can control expiry without sleeping.
	now func() time.Time
}

func newServer(cfg config, store *Store, rn *renderer, trust *proxyTrust) *server {
	return &server{cfg: cfg, store: store, rn: rn, trust: trust, now: time.Now}
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	// A literal pattern beats a wildcard, so /robots.txt and /healthz are
	// never mistaken for a short code. The {$} anchors the landing page to
	// the root instead of matching every path as a prefix.
	mux.HandleFunc("GET /{$}", s.handleHome)
	mux.HandleFunc("GET /robots.txt", s.handleRobots)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.Handle("GET /static/{file}", s.rn.assets)
	mux.HandleFunc("GET /{code}", s.handleCode)
	return mux
}

type homeData struct {
	page
	Host string
	Repo string
}

func (s *server) handleHome(w http.ResponseWriter, r *http.Request) {
	s.rn.render(w, http.StatusOK, "home", homeData{
		page: page{Title: "teenyurl"},
		Host: s.cfg.host(),
		Repo: repoURL,
	})
}

// handleRobots keeps short codes out of search indexes.
//
// The Disallow line matters more than the Allow. Without it, a crawler that
// finds a short link anywhere follows it and the code lands in an index, which
// turns an unguessable link into a public one.
func (s *server) handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write([]byte("User-agent: *\nAllow: /$\nDisallow: /\n"))
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte("ok\n"))
}

// handleCode serves both the redirect and the preview page.
func (s *server) handleCode(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")

	// In a URL path a plus is a literal character, unlike in a query string.
	// A trailing plus asks for the preview instead of the redirect.
	preview := strings.HasSuffix(code, "+")
	code = strings.TrimSuffix(code, "+")
	if code == "" {
		s.notFound(w)
		return
	}

	l, ok := s.store.Get(code)
	if !ok {
		s.notFound(w)
		return
	}
	if l.Expired(s.now()) {
		s.gone(w)
		return
	}
	if preview {
		s.showPreview(w, l)
		return
	}

	s.store.RecordClick(code, s.now())

	// A 301 is cached permanently, so the second visit never reaches the
	// server. That breaks both the click count and editing the target, which
	// is the main reason to self-host a shortener. Some browsers cache a 302
	// heuristically, hence no-store.
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	// Without this the destination learns which page sent the visitor here.
	h.Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, l.URL, http.StatusFound)
}

type previewData struct {
	page
	Link *Link
	Host string
}

func (s *server) showPreview(w http.ResponseWriter, l *Link) {
	// The preview is a look, not a visit, so it does not count as a click.
	w.Header().Set("Referrer-Policy", "no-referrer")
	s.rn.render(w, http.StatusOK, "preview", previewData{
		page: page{Title: "Where " + l.Code + " goes", NoIndex: true},
		Link: l,
		Host: hostOf(l.URL),
	})
}

type errorData struct {
	page
	Status  int
	Heading string
	Message string
}

func (s *server) notFound(w http.ResponseWriter) {
	s.rn.render(w, http.StatusNotFound, "error", errorData{
		page:    page{Title: "Not found", NoIndex: true},
		Status:  http.StatusNotFound,
		Heading: "No such link",
		Message: "This short link does not exist. Check the address for a typo.",
	})
}

func (s *server) gone(w http.ResponseWriter) {
	s.rn.render(w, http.StatusGone, "error", errorData{
		page:    page{Title: "Expired", NoIndex: true},
		Status:  http.StatusGone,
		Heading: "This link has expired",
		Message: "The link was set to stop working, and that time has passed.",
	})
}

// hostOf returns the host of a URL for display, or the whole URL if it will
// not parse. The stored URL has already passed validateURL, so the fallback
// only guards against a hand-edited data file.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Host
}
