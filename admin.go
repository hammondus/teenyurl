package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// maxFormBytes bounds an admin form post. A URL plus a note is small; the
// limit stops a stray large body from being buffered.
const maxFormBytes = 64 << 10

func (s *server) adminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin", s.guard(s.handleAdmin))
	mux.HandleFunc("GET /admin/{$}", s.guard(s.handleAdmin))
	mux.HandleFunc("POST /admin/login", s.handleLogin)
	mux.HandleFunc("POST /admin/logout", s.guard(s.handleLogout))
	mux.HandleFunc("POST /admin/links", s.guard(s.handleCreate))
	mux.HandleFunc("POST /admin/links/{code}/update", s.guard(s.handleUpdate))
	mux.HandleFunc("POST /admin/links/{code}/delete", s.guard(s.handleDelete))
}

// guard requires a live session, checks the form token on writes, and sets the
// cache policy for every admin response.
func (s *server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// Admin pages are per-session, so private keeps a shared cache or
		// proxy from ever storing one.
		h.Set("Cache-Control", "no-cache, private")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")

		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
		}

		sess, ok := s.auth.current(r)
		if !ok {
			if r.Method == http.MethodGet {
				s.showLogin(w, http.StatusOK, "")
				return
			}
			http.Error(w, "Your session expired. Sign in again.", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost && !csrfMatches(sess, r) {
			http.Error(w, "Form token mismatch. Reload the page and try again.", http.StatusForbidden)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, sess)))
	}
}

type loginData struct {
	page
	Message string
}

func (s *server) showLogin(w http.ResponseWriter, status int, message string) {
	s.rn.render(w, status, "login", loginData{
		page:    page{Title: "Sign in — teenyurl", NoIndex: true, Styles: []string{"admin.css"}},
		Message: message,
	})
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store, private")
	h.Set("X-Frame-Options", "DENY")

	addr := s.trust.clientIP(r)
	if s.auth.rateLimited(addr) {
		log.Printf("admin: login rate limit reached for %s", addr)
		s.showLogin(w, http.StatusTooManyRequests,
			"Too many attempts. Wait fifteen minutes and try again.")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if !s.auth.passwordMatches(r.PostFormValue("password")) {
		log.Printf("admin: failed login from %s", addr)
		s.showLogin(w, http.StatusUnauthorized, "That password is wrong.")
		return
	}
	s.auth.clearAttempts(addr)
	if err := s.auth.start(w); err != nil {
		log.Printf("admin: start session: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	log.Printf("admin: signed in from %s", addr)
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.auth.end(w, r)
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// adminLink is one row of the link table.
type adminLink struct {
	LinkView
	Short   string
	Expired bool
}

type adminData struct {
	page
	CSRF     string
	BaseURL  string
	Host     string
	Links    []adminLink
	Flash    string
	ErrorMsg string
	Form     linkForm
}

func (s *server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	s.showAdmin(w, r, http.StatusOK, s.flashFrom(r.URL.Query()), "", linkForm{})
}

// flashFrom builds the confirmation line from the query the redirect carried.
// The verbs are fixed here rather than taken from the URL, so the page can
// never be made to display arbitrary text.
func (s *server) flashFrom(q url.Values) string {
	for _, verb := range []string{"Created", "Updated", "Deleted"} {
		if code := q.Get(strings.ToLower(verb)); code != "" {
			return fmt.Sprintf("%s %s.", verb, code)
		}
	}
	return ""
}

func (s *server) showAdmin(w http.ResponseWriter, r *http.Request, status int, flash, errMsg string, form linkForm) {
	now := s.now()
	views := s.store.List()
	links := make([]adminLink, 0, len(views))
	for _, v := range views {
		links = append(links, adminLink{
			LinkView: v,
			Short:    s.cfg.baseURL + "/" + v.Code,
			Expired:  v.Expired(now),
		})
	}
	s.rn.render(w, status, "admin", adminData{
		page: page{
			Title: "Links — teenyurl", NoIndex: true,
			Styles: []string{"admin.css"}, Scripts: []string{"admin.js"},
		},
		CSRF:     sessionFrom(r.Context()).csrf,
		BaseURL:  s.cfg.baseURL,
		Host:     s.cfg.host(),
		Links:    links,
		Flash:    flash,
		ErrorMsg: errMsg,
		Form:     form,
	})
}

// linkForm is the raw text of the create or edit form, kept so that a rejected
// submission can be shown back to the user with their input intact.
type linkForm struct {
	Code    string
	URL     string
	Note    string
	Expires string
	TZOff   string
}

func readLinkForm(r *http.Request) linkForm {
	return linkForm{
		Code:    strings.TrimSpace(r.PostFormValue("code")),
		URL:     strings.TrimSpace(r.PostFormValue("url")),
		Note:    strings.TrimSpace(r.PostFormValue("note")),
		Expires: strings.TrimSpace(r.PostFormValue("expires")),
		TZOff:   strings.TrimSpace(r.PostFormValue("tz_offset")),
	}
}

// parse validates the form and returns the values the store needs.
func (f linkForm) parse() (rawURL string, expires *time.Time, err error) {
	if f.Code != "" {
		if err := validateCode(f.Code); err != nil {
			return "", nil, err
		}
	}
	if rawURL, err = validateURL(f.URL); err != nil {
		return "", nil, err
	}
	if expires, err = parseExpiry(f.Expires, f.TZOff); err != nil {
		return "", nil, err
	}
	return rawURL, expires, nil
}

// parseExpiry reads a datetime-local value.
//
// A datetime-local input carries no time zone. The page posts the browser's
// offset in minutes, using the same sign as Date.getTimezoneOffset: minutes to
// add to local time to reach UTC. With no offset, which means the page ran
// without JavaScript, the value is read as UTC.
func parseExpiry(value, offset string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02T15:04", value)
	if err != nil {
		// Some browsers add seconds to the value.
		t, err = time.Parse("2006-01-02T15:04:05", value)
		if err != nil {
			return nil, fmt.Errorf("expiry %q is not a date and time", value)
		}
	}
	if offset != "" {
		m, err := strconv.Atoi(offset)
		if err != nil {
			return nil, fmt.Errorf("time zone offset %q is not a number", offset)
		}
		if m < -1440 || m > 1440 {
			return nil, fmt.Errorf("time zone offset %d minutes is out of range", m)
		}
		t = t.Add(time.Duration(m) * time.Minute)
	}
	t = t.UTC()
	return &t, nil
}

func (s *server) handleCreate(w http.ResponseWriter, r *http.Request) {
	form := readLinkForm(r)
	rawURL, expires, err := form.parse()
	if err != nil {
		s.showAdmin(w, r, http.StatusBadRequest, "", err.Error(), form)
		return
	}
	l, err := s.store.Create(form.Code, rawURL, form.Note, expires, s.now())
	if errors.Is(err, ErrCodeTaken) {
		s.showAdmin(w, r, http.StatusConflict, "", fmt.Sprintf("%s is already in use.", form.Code), form)
		return
	}
	if err != nil {
		log.Printf("admin: create link: %v", err)
		s.showAdmin(w, r, http.StatusInternalServerError, "", "Could not save the link.", form)
		return
	}
	http.Redirect(w, r, "/admin?created="+url.QueryEscape(l.Code), http.StatusSeeOther)
}

func (s *server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	form := readLinkForm(r)
	form.Code = code
	rawURL, expires, err := form.parse()
	if err != nil {
		s.showAdmin(w, r, http.StatusBadRequest, "", fmt.Sprintf("%s: %s", code, err), form)
		return
	}
	_, err = s.store.Update(code, rawURL, form.Note, expires, s.now())
	if errors.Is(err, ErrNotFound) {
		s.showAdmin(w, r, http.StatusNotFound, "", fmt.Sprintf("%s no longer exists.", code), linkForm{})
		return
	}
	if err != nil {
		log.Printf("admin: update %s: %v", code, err)
		s.showAdmin(w, r, http.StatusInternalServerError, "", "Could not save the link.", form)
		return
	}
	http.Redirect(w, r, "/admin?updated="+url.QueryEscape(code), http.StatusSeeOther)
}

func (s *server) handleDelete(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	err := s.store.Delete(code, s.now())
	if errors.Is(err, ErrNotFound) {
		s.showAdmin(w, r, http.StatusNotFound, "", fmt.Sprintf("%s no longer exists.", code), linkForm{})
		return
	}
	if err != nil {
		log.Printf("admin: delete %s: %v", code, err)
		s.showAdmin(w, r, http.StatusInternalServerError, "", "Could not delete the link.", linkForm{})
		return
	}
	log.Printf("admin: deleted %s", code)
	http.Redirect(w, r, "/admin?deleted="+url.QueryEscape(code), http.StatusSeeOther)
}
