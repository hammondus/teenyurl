package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testServer builds a server backed by a temporary data directory, with a
// fixed clock so that expiry is decided rather than raced.
func testServer(t *testing.T) *server {
	t.Helper()
	rn, err := newRenderer()
	if err != nil {
		t.Fatalf("newRenderer: %v", err)
	}
	store := newStore(t, t.TempDir())
	trust, err := parseTrustedProxies("127.0.0.1/32")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config{
		baseURL:       "https://url.hammond.zone",
		codeLen:       6,
		adminPassword: testPassword,
		sessionTTL:    24 * time.Hour,
	}
	s := newServer(cfg, store, rn, trust)
	s.now = func() time.Time { return testTime }
	s.auth.now = s.now
	return s
}

func get(t *testing.T, s *server, target string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
	return w
}

func TestRedirect(t *testing.T) {
	s := testServer(t)
	if _, err := s.store.Create("docs", "https://example.com/manual", "", nil, testTime); err != nil {
		t.Fatal(err)
	}

	w := get(t, s, "/docs")
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusFound)
	}
	if got := w.Header().Get("Location"); got != "https://example.com/manual" {
		t.Errorf("Location = %q", got)
	}
	// A 301 would be cached permanently and the second visit would never
	// arrive, taking the click count and the editable target with it.
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := w.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
	if n, last := s.store.Clicks("docs"); n != 1 || !last.Equal(testTime) {
		t.Errorf("clicks = %d at %v, want 1 at %v", n, last, testTime)
	}
}

func TestRedirectUnknownCode(t *testing.T) {
	s := testServer(t)
	w := get(t, s, "/nothere")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want HTML", ct)
	}
}

func TestRedirectExpiredLink(t *testing.T) {
	s := testServer(t)
	past := testTime.Add(-time.Minute)
	if _, err := s.store.Create("old", "https://example.com", "", &past, testTime); err != nil {
		t.Fatal(err)
	}

	w := get(t, s, "/old")
	if w.Code != http.StatusGone {
		t.Errorf("status = %d, want %d", w.Code, http.StatusGone)
	}
	if n, _ := s.store.Clicks("old"); n != 0 {
		t.Errorf("clicks = %d, want 0 for an expired link", n)
	}
}

func TestRedirectLinkExpiringLater(t *testing.T) {
	s := testServer(t)
	future := testTime.Add(time.Minute)
	if _, err := s.store.Create("soon", "https://example.com", "", &future, testTime); err != nil {
		t.Fatal(err)
	}
	if w := get(t, s, "/soon"); w.Code != http.StatusFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusFound)
	}
}

func TestPreviewPage(t *testing.T) {
	s := testServer(t)
	if _, err := s.store.Create("docs", "https://example.com/manual", "the manual", nil, testTime); err != nil {
		t.Fatal(err)
	}

	w := get(t, s, "/docs+")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"example.com", "https://example.com/manual", "the manual"} {
		if !strings.Contains(body, want) {
			t.Errorf("preview page does not mention %q", want)
		}
	}
	if !strings.Contains(body, `content="noindex, nofollow"`) {
		t.Error("preview page is missing the noindex directive")
	}
	// A preview is a look, not a visit.
	if n, _ := s.store.Clicks("docs"); n != 0 {
		t.Errorf("clicks = %d, want 0 for a preview", n)
	}
}

func TestPreviewOfExpiredLink(t *testing.T) {
	s := testServer(t)
	past := testTime.Add(-time.Minute)
	if _, err := s.store.Create("old", "https://example.com", "", &past, testTime); err != nil {
		t.Fatal(err)
	}
	if w := get(t, s, "/old+"); w.Code != http.StatusGone {
		t.Errorf("status = %d, want %d", w.Code, http.StatusGone)
	}
}

func TestPreviewEscapesTheDestination(t *testing.T) {
	// The destination is rendered inside the page, so it must be escaped.
	// validateURL already rejects javascript: and data:, but a hand-edited
	// data file must not become stored cross-site scripting.
	s := testServer(t)
	raw := `https://example.com/"><script>alert(1)</script>`
	if _, err := s.store.Create("x", raw, "", nil, testTime); err != nil {
		t.Fatal(err)
	}
	body := get(t, s, "/x+").Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("the destination was rendered unescaped")
	}
}

func TestBarePlusIsNotFound(t *testing.T) {
	s := testServer(t)
	if w := get(t, s, "/+"); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHomePage(t *testing.T) {
	s := testServer(t)
	w := get(t, s, "/")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, repoURL) {
		t.Errorf("landing page does not link to %s", repoURL)
	}
	if !strings.Contains(body, "url.hammond.zone") {
		t.Error("landing page does not name the host")
	}
	// A page carries the URLs of the assets it references, so serving it
	// stale defeats asset cache busting.
	if got := w.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
}

func TestRobotsTxt(t *testing.T) {
	s := testServer(t)
	w := get(t, s, "/robots.txt")
	body := w.Body.String()
	if !strings.Contains(body, "Disallow: /") {
		t.Errorf("robots.txt does not disallow crawling short codes:\n%s", body)
	}
	if !strings.Contains(body, "Allow: /$") {
		t.Errorf("robots.txt does not allow the landing page:\n%s", body)
	}
}

func TestHealthz(t *testing.T) {
	s := testServer(t)
	w := get(t, s, "/healthz")
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "ok" {
		t.Errorf("healthz returned %d %q", w.Code, w.Body.String())
	}
}

func TestReservedPathsBeatStoredCodes(t *testing.T) {
	// validateCode blocks these aliases at the admin layer, but a hand-edited
	// data file must not be able to shadow the server's own paths either.
	s := testServer(t)
	for _, code := range []string{"healthz", "robots.txt", "static"} {
		if _, err := s.store.Create(code, "https://evil.example", "", nil, testTime); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"/healthz", "/robots.txt"} {
		w := get(t, s, path)
		if w.Code == http.StatusFound {
			t.Errorf("%s redirected to a stored link", path)
		}
	}
}

func TestNonGetMethodIsRejected(t *testing.T) {
	s := testServer(t)
	if _, err := s.store.Create("docs", "https://example.com", "", nil, testTime); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/docs", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHeadFollowsTheSameRoute(t *testing.T) {
	s := testServer(t)
	if _, err := s.store.Create("docs", "https://example.com", "", nil, testTime); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, httptest.NewRequest(http.MethodHead, "/docs", nil))
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusFound)
	}
}

func TestStaticAssetCaching(t *testing.T) {
	s := testServer(t)
	hash := s.rn.assets.files["app.css"].hash

	versioned := get(t, s, "/static/app.css?v="+hash)
	if versioned.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", versioned.Code)
	}
	if got, want := versioned.Header().Get("Cache-Control"), "public, max-age=31536000, immutable"; got != want {
		t.Errorf("versioned Cache-Control = %q, want %q", got, want)
	}
	if got := versioned.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/css") {
		t.Errorf("Content-Type = %q, want text/css", got)
	}

	// A bookmarked unversioned path can change under the client, so it gets a
	// short lifetime instead.
	bare := get(t, s, "/static/app.css")
	if got, want := bare.Header().Get("Cache-Control"), "public, max-age=3600"; got != want {
		t.Errorf("unversioned Cache-Control = %q, want %q", got, want)
	}

	if got := versioned.Header().Get("ETag"); got != `"`+hash+`"` {
		t.Errorf("ETag = %q, want the content hash", got)
	}
}

func TestStaticAssetRevalidates(t *testing.T) {
	s := testServer(t)
	hash := s.rn.assets.files["app.css"].hash

	r := httptest.NewRequest(http.MethodGet, "/static/app.css?v="+hash, nil)
	r.Header.Set("If-None-Match", `"`+hash+`"`)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotModified {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotModified)
	}
}

func TestStaticAssetNotFound(t *testing.T) {
	s := testServer(t)
	if w := get(t, s, "/static/nothing.css"); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestAssetURLsAreVersioned(t *testing.T) {
	s := testServer(t)
	body := get(t, s, "/").Body.String()
	want := s.rn.assets.url("app.css")
	if !strings.Contains(body, want) {
		t.Errorf("landing page does not reference %s", want)
	}
	if !strings.Contains(want, "?v=") {
		t.Errorf("asset URL %q is not cache busted", want)
	}
}
