package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

var csrfPattern = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

// signIn logs in and returns the session cookie and a form token from the
// admin page, which is how a browser gets both.
func signIn(t *testing.T, s *server) (*http.Cookie, string) {
	t.Helper()
	w := post(t, s, "/admin/login", url.Values{"password": {testPassword}}, nil, "")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	var cookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("login set no session cookie")
	}

	r := httptest.NewRequest(http.MethodGet, "/admin", nil)
	r.AddCookie(cookie)
	page := httptest.NewRecorder()
	s.routes().ServeHTTP(page, r)
	m := csrfPattern.FindStringSubmatch(page.Body.String())
	if m == nil {
		t.Fatalf("admin page carries no form token:\n%s", page.Body.String())
	}
	return cookie, m[1]
}

func post(t *testing.T, s *server, target string, form url.Values, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	if csrf != "" {
		form.Set("csrf", csrf)
	}
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	return w
}

func getAs(t *testing.T, s *server, target string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, target, nil)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	return w
}

func TestAdminRequiresSignIn(t *testing.T) {
	s := testServer(t)
	w := get(t, s, "/admin")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `name="password"`) {
		t.Error("an unauthenticated visitor did not get the sign-in form")
	}
	if strings.Contains(body, "New link") {
		t.Error("an unauthenticated visitor saw the link form")
	}
}

func TestAdminWriteWithoutSessionIsForbidden(t *testing.T) {
	s := testServer(t)
	w := post(t, s, "/admin/links", url.Values{"url": {"https://example.com"}}, nil, "")
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
	if len(s.store.List()) != 0 {
		t.Error("an unauthenticated post created a link")
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	s := testServer(t)
	w := post(t, s, "/admin/login", url.Values{"password": {"not the password"}}, nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			t.Error("a failed login set a session cookie")
		}
	}
}

func TestLoginRateLimit(t *testing.T) {
	s := testServer(t)
	for i := 0; i < loginAttempts; i++ {
		if w := post(t, s, "/admin/login", url.Values{"password": {"wrong"}}, nil, ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i+1, w.Code)
		}
	}
	w := post(t, s, "/admin/login", url.Values{"password": {"wrong"}}, nil, "")
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want %d", w.Code, http.StatusTooManyRequests)
	}
	// The limit must hold even once the password is right, or it would only
	// slow down an attacker who never guesses correctly.
	w = post(t, s, "/admin/login", url.Values{"password": {testPassword}}, nil, "")
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("correct password after the limit: status = %d, want %d", w.Code, http.StatusTooManyRequests)
	}
}

func TestLoginRateLimitIsPerClientAddress(t *testing.T) {
	// The limit keys on the address that clientIP resolves, so a forged
	// X-Forwarded-For from an untrusted peer must not create a fresh bucket.
	s := testServer(t)
	for i := 0; i < loginAttempts; i++ {
		post(t, s, "/admin/login", url.Values{"password": {"wrong"}}, nil, "")
	}
	r := httptest.NewRequest(http.MethodPost, "/admin/login",
		strings.NewReader(url.Values{"password": {"wrong"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-Forwarded-For", "203.0.113.99")
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want %d: a forged header reset the limit", w.Code, http.StatusTooManyRequests)
	}
}

func TestSessionCookieFlags(t *testing.T) {
	s := testServer(t)
	w := post(t, s, "/admin/login", url.Values{"password": {testPassword}}, nil, "")
	var c *http.Cookie
	for _, got := range w.Result().Cookies() {
		if got.Name == sessionCookie {
			c = got
		}
	}
	if c == nil {
		t.Fatal("no session cookie")
	}
	if !c.HttpOnly {
		t.Error("cookie is not HttpOnly, so a script could read it")
	}
	if !c.Secure {
		t.Error("cookie is not Secure under an https base URL")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", c.SameSite)
	}
	if c.Path != "/admin" {
		t.Errorf("Path = %q, want /admin", c.Path)
	}
}

func TestAdminPageIsNotCacheable(t *testing.T) {
	s := testServer(t)
	cookie, _ := signIn(t, s)
	w := getAs(t, s, "/admin", cookie)
	if got, want := w.Header().Get("Cache-Control"), "no-cache, private"; got != want {
		t.Errorf("Cache-Control = %q, want %q", got, want)
	}
	if !strings.Contains(w.Body.String(), `content="noindex, nofollow"`) {
		t.Error("the admin page is missing the noindex directive")
	}
}

func TestCreateLink(t *testing.T) {
	s := testServer(t)
	cookie, csrf := signIn(t, s)

	w := post(t, s, "/admin/links", url.Values{
		"url":  {"https://example.com/manual"},
		"code": {"docs"},
		"note": {"the manual"},
	}, cookie, csrf)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d: %s", w.Code, http.StatusSeeOther, w.Body.String())
	}
	if got := w.Header().Get("Location"); got != "/admin?created=docs" {
		t.Errorf("Location = %q", got)
	}
	l, ok := s.store.Get("docs")
	if !ok {
		t.Fatal("the link was not stored")
	}
	if l.URL != "https://example.com/manual" || l.Note != "the manual" {
		t.Errorf("stored %+v", l)
	}
	// The new link must redirect straight away.
	if w := get(t, s, "/docs"); w.Code != http.StatusFound {
		t.Errorf("redirect status = %d, want 302", w.Code)
	}
}

func TestCreateGeneratesACode(t *testing.T) {
	s := testServer(t)
	cookie, csrf := signIn(t, s)
	w := post(t, s, "/admin/links", url.Values{"url": {"https://example.com"}}, cookie, csrf)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	links := s.store.List()
	if len(links) != 1 || len(links[0].Code) != s.cfg.codeLen {
		t.Errorf("stored %+v, want one link with a %d character code", links, s.cfg.codeLen)
	}
}

func TestCreateRejectsBadInput(t *testing.T) {
	tests := []struct {
		name   string
		form   url.Values
		status int
	}{
		{"no scheme", url.Values{"url": {"example.com"}}, http.StatusBadRequest},
		{"javascript scheme", url.Values{"url": {"javascript:alert(1)"}}, http.StatusBadRequest},
		{"empty url", url.Values{"url": {""}}, http.StatusBadRequest},
		{"reserved code", url.Values{"url": {"https://example.com"}, "code": {"admin"}}, http.StatusBadRequest},
		{"code with a slash", url.Values{"url": {"https://example.com"}, "code": {"a/b"}}, http.StatusBadRequest},
		{"unparseable expiry", url.Values{"url": {"https://example.com"}, "expires": {"tomorrow"}}, http.StatusBadRequest},
		{"absurd offset", url.Values{"url": {"https://example.com"}, "expires": {"2026-09-01T10:00"}, "tz_offset": {"99999"}}, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testServer(t)
			cookie, csrf := signIn(t, s)
			w := post(t, s, "/admin/links", tt.form, cookie, csrf)
			if w.Code != tt.status {
				t.Errorf("status = %d, want %d", w.Code, tt.status)
			}
			if len(s.store.List()) != 0 {
				t.Error("a rejected form still created a link")
			}
		})
	}
}

func TestCreateKeepsInputAfterAnError(t *testing.T) {
	s := testServer(t)
	cookie, csrf := signIn(t, s)
	w := post(t, s, "/admin/links", url.Values{
		"url":  {"example.com"},
		"note": {"worth keeping"},
	}, cookie, csrf)
	body := w.Body.String()
	if !strings.Contains(body, "worth keeping") {
		t.Error("the rejected form lost the note the user typed")
	}
	if !strings.Contains(body, "example.com") {
		t.Error("the rejected form lost the URL the user typed")
	}
}

func TestCreateRejectsDuplicateCode(t *testing.T) {
	s := testServer(t)
	cookie, csrf := signIn(t, s)
	form := url.Values{"url": {"https://example.com"}, "code": {"docs"}}
	if w := post(t, s, "/admin/links", form, cookie, csrf); w.Code != http.StatusSeeOther {
		t.Fatalf("first create: %d", w.Code)
	}
	w := post(t, s, "/admin/links", form, cookie, csrf)
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d", w.Code, http.StatusConflict)
	}
}

func TestCreateWithExpiryAppliesTheTimeZone(t *testing.T) {
	s := testServer(t)
	cookie, csrf := signIn(t, s)
	// -600 is the offset a browser in UTC+10 reports, so 12:00 local is
	// 02:00 UTC.
	w := post(t, s, "/admin/links", url.Values{
		"url":       {"https://example.com"},
		"code":      {"tz"},
		"expires":   {"2026-09-01T12:00"},
		"tz_offset": {"-600"},
	}, cookie, csrf)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	l, _ := s.store.Get("tz")
	want := time.Date(2026, 9, 1, 2, 0, 0, 0, time.UTC)
	if l.ExpiresAt == nil || !l.ExpiresAt.Equal(want) {
		t.Errorf("expiry = %v, want %v", l.ExpiresAt, want)
	}
}

func TestMissingFormTokenIsRejected(t *testing.T) {
	s := testServer(t)
	cookie, _ := signIn(t, s)
	w := post(t, s, "/admin/links", url.Values{"url": {"https://example.com"}}, cookie, "")
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
	if len(s.store.List()) != 0 {
		t.Error("a post with no form token created a link")
	}
}

func TestWrongFormTokenIsRejected(t *testing.T) {
	s := testServer(t)
	cookie, _ := signIn(t, s)
	w := post(t, s, "/admin/links", url.Values{"url": {"https://example.com"}}, cookie, "borrowed-from-elsewhere")
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestUpdateLink(t *testing.T) {
	s := testServer(t)
	cookie, csrf := signIn(t, s)
	if _, err := s.store.Create("docs", "https://old.example", "old", nil, testTime); err != nil {
		t.Fatal(err)
	}

	w := post(t, s, "/admin/links/docs/update", url.Values{
		"url":  {"https://new.example"},
		"note": {"new"},
	}, cookie, csrf)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	l, _ := s.store.Get("docs")
	if l.URL != "https://new.example" || l.Note != "new" {
		t.Errorf("stored %+v", l)
	}
	// Editing the target is the point of self-hosting, so the same code must
	// now send visitors somewhere else.
	redirect := get(t, s, "/docs")
	if got := redirect.Header().Get("Location"); got != "https://new.example" {
		t.Errorf("Location = %q, want the new target", got)
	}
}

func TestUpdateClearsAnExpiry(t *testing.T) {
	s := testServer(t)
	cookie, csrf := signIn(t, s)
	past := testTime.Add(-time.Hour)
	if _, err := s.store.Create("docs", "https://example.com", "", &past, testTime); err != nil {
		t.Fatal(err)
	}
	w := post(t, s, "/admin/links/docs/update", url.Values{
		"url":     {"https://example.com"},
		"expires": {""},
	}, cookie, csrf)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", w.Code)
	}
	if l, _ := s.store.Get("docs"); l.ExpiresAt != nil {
		t.Errorf("expiry = %v, want none", l.ExpiresAt)
	}
	if w := get(t, s, "/docs"); w.Code != http.StatusFound {
		t.Errorf("status = %d, want the link live again", w.Code)
	}
}

func TestUpdateUnknownCode(t *testing.T) {
	s := testServer(t)
	cookie, csrf := signIn(t, s)
	w := post(t, s, "/admin/links/missing/update", url.Values{"url": {"https://example.com"}}, cookie, csrf)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestDeleteLink(t *testing.T) {
	s := testServer(t)
	cookie, csrf := signIn(t, s)
	if _, err := s.store.Create("docs", "https://example.com", "", nil, testTime); err != nil {
		t.Fatal(err)
	}
	w := post(t, s, "/admin/links/docs/delete", url.Values{}, cookie, csrf)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if _, ok := s.store.Get("docs"); ok {
		t.Error("the link is still present")
	}
	if w := get(t, s, "/docs"); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 after deletion", w.Code)
	}
}

func TestLogoutEndsTheSession(t *testing.T) {
	s := testServer(t)
	cookie, csrf := signIn(t, s)
	if w := post(t, s, "/admin/logout", url.Values{}, cookie, csrf); w.Code != http.StatusSeeOther {
		t.Fatalf("logout status = %d", w.Code)
	}
	w := getAs(t, s, "/admin", cookie)
	if !strings.Contains(w.Body.String(), `name="password"`) {
		t.Error("the old cookie still opens the admin page")
	}
}

func TestExpiredSessionIsRejected(t *testing.T) {
	s := testServer(t)
	cookie, _ := signIn(t, s)
	// Move the clock past the session lifetime.
	s.auth.now = func() time.Time { return testTime.Add(25 * time.Hour) }
	w := getAs(t, s, "/admin", cookie)
	if !strings.Contains(w.Body.String(), `name="password"`) {
		t.Error("an expired session still opens the admin page")
	}
}

func TestAdminListShowsLinks(t *testing.T) {
	s := testServer(t)
	cookie, _ := signIn(t, s)
	if _, err := s.store.Create("docs", "https://example.com/manual", "the manual", nil, testTime); err != nil {
		t.Fatal(err)
	}
	s.store.RecordClick("docs", testTime)

	body := getAs(t, s, "/admin", cookie).Body.String()
	for _, want := range []string{"docs", "https://example.com/manual", "the manual",
		"https://url.example.com/docs", "1 click"} {
		if !strings.Contains(body, want) {
			t.Errorf("the admin page does not show %q", want)
		}
	}
}

func TestFlashTextIsNotTakenFromTheQuery(t *testing.T) {
	// The verb is fixed in the handler, so a crafted query cannot put
	// arbitrary wording on the page.
	s := testServer(t)
	cookie, _ := signIn(t, s)
	body := getAs(t, s, "/admin?created=docs&updated=%3Cb%3Eboo%3C%2Fb%3E", cookie).Body.String()
	if !strings.Contains(body, "Created docs.") {
		t.Error("the confirmation line is missing")
	}
	if strings.Contains(body, "<b>boo</b>") {
		t.Error("query text was rendered unescaped")
	}
}

func TestParseExpiry(t *testing.T) {
	tests := []struct {
		value, offset string
		want          time.Time
		nilWant, fail bool
	}{
		{value: "", nilWant: true},
		{value: "2026-09-01T12:00", want: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)},
		{value: "2026-09-01T12:00:30", want: time.Date(2026, 9, 1, 12, 0, 30, 0, time.UTC)},
		{value: "2026-09-01T12:00", offset: "-600", want: time.Date(2026, 9, 1, 2, 0, 0, 0, time.UTC)},
		{value: "2026-09-01T12:00", offset: "300", want: time.Date(2026, 9, 1, 17, 0, 0, 0, time.UTC)},
		{value: "2026-09-01T12:00", offset: "0", want: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)},
		{value: "tomorrow", fail: true},
		{value: "2026-09-01", fail: true},
		{value: "2026-09-01T12:00", offset: "soon", fail: true},
		{value: "2026-09-01T12:00", offset: "2000", fail: true},
	}
	for _, tt := range tests {
		got, err := parseExpiry(tt.value, tt.offset)
		if tt.fail {
			if err == nil {
				t.Errorf("parseExpiry(%q, %q) returned no error", tt.value, tt.offset)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseExpiry(%q, %q) = %v", tt.value, tt.offset, err)
			continue
		}
		if tt.nilWant {
			if got != nil {
				t.Errorf("parseExpiry(%q, %q) = %v, want nil", tt.value, tt.offset, got)
			}
			continue
		}
		if got == nil || !got.Equal(tt.want) {
			t.Errorf("parseExpiry(%q, %q) = %v, want %v", tt.value, tt.offset, got, tt.want)
		}
	}
}
