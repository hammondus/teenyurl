package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/netip"
	"sync"
	"time"
)

const sessionCookie = "teenyurl_session"

// minPasswordLen is the only real protection the password itself carries: the
// value is compared as-is rather than hashed, because there is no password
// database to protect. See DESIGN-DECISIONS.md.
const minPasswordLen = 12

// loginWindow and loginAttempts bound password guessing per client address.
const (
	loginWindow   = 15 * time.Minute
	loginAttempts = 5
)

// session is one signed-in browser.
type session struct {
	csrf    string
	expires time.Time
}

// attemptBucket counts failed logins from one address within a fixed window.
type attemptBucket struct {
	count int
	reset time.Time
}

// authenticator holds the single admin credential, the live sessions, and the
// login rate limiter.
type authenticator struct {
	hash   [32]byte
	ttl    time.Duration
	secure bool
	trust  *proxyTrust
	now    func() time.Time

	mu       sync.Mutex
	sessions map[string]session
	attempts map[netip.Addr]*attemptBucket
}

func newAuthenticator(password string, ttl time.Duration, secure bool, trust *proxyTrust) *authenticator {
	return &authenticator{
		// Hashing both sides before the comparison keeps the lengths equal,
		// so the comparison leaks neither the password nor its length.
		hash:     sha256.Sum256([]byte(password)),
		ttl:      ttl,
		secure:   secure,
		trust:    trust,
		now:      time.Now,
		sessions: make(map[string]session),
		attempts: make(map[netip.Addr]*attemptBucket),
	}
}

func (a *authenticator) passwordMatches(attempt string) bool {
	got := sha256.Sum256([]byte(attempt))
	return subtle.ConstantTimeCompare(got[:], a.hash[:]) == 1
}

// rateLimited reports whether the address has spent its attempts, and counts
// this one. It prunes expired buckets on the way through, so the map stays
// proportional to recent traffic rather than to all traffic ever.
func (a *authenticator) rateLimited(addr netip.Addr) bool {
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()

	for k, b := range a.attempts {
		if now.After(b.reset) {
			delete(a.attempts, k)
		}
	}
	b, ok := a.attempts[addr]
	if !ok || now.After(b.reset) {
		b = &attemptBucket{reset: now.Add(loginWindow)}
		a.attempts[addr] = b
	}
	if b.count >= loginAttempts {
		return true
	}
	b.count++
	return false
}

// clearAttempts forgets the failures for an address after a correct password.
func (a *authenticator) clearAttempts(addr netip.Addr) {
	a.mu.Lock()
	delete(a.attempts, addr)
	a.mu.Unlock()
}

// start creates a session and sets its cookie.
func (a *authenticator) start(w http.ResponseWriter) error {
	token, err := randomToken()
	if err != nil {
		return err
	}
	csrf, err := randomToken()
	if err != nil {
		return err
	}
	now := a.now()

	a.mu.Lock()
	for k, s := range a.sessions {
		if now.After(s.expires) {
			delete(a.sessions, k)
		}
	}
	a.sessions[token] = session{csrf: csrf, expires: now.Add(a.ttl)}
	a.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:  sessionCookie,
		Value: token,
		Path:  "/admin",
		// Lax is the primary defence against a cross-site form post. The CSRF
		// token in each form backs it up.
		SameSite: http.SameSiteLaxMode,
		HttpOnly: true,
		Secure:   a.secure,
		Expires:  now.Add(a.ttl),
		MaxAge:   int(a.ttl / time.Second),
	})
	return nil
}

// current returns the session for a request, if it has a live one.
func (a *authenticator) current(r *http.Request) (session, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return session{}, false
	}
	a.mu.Lock()
	s, ok := a.sessions[c.Value]
	if ok && a.now().After(s.expires) {
		delete(a.sessions, c.Value)
		ok = false
	}
	a.mu.Unlock()
	return s, ok
}

// end drops the session and clears the cookie.
func (a *authenticator) end(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		a.mu.Lock()
		delete(a.sessions, c.Value)
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/admin",
		SameSite: http.SameSiteLaxMode,
		HttpOnly: true,
		Secure:   a.secure,
		MaxAge:   -1,
	})
}

// csrfMatches compares the token a form carried with the one the session holds.
func csrfMatches(s session, r *http.Request) bool {
	return subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(s.csrf)) == 1
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// sessionKey carries the caller's session from the middleware to the handler.
type sessionKey struct{}

func sessionFrom(ctx context.Context) session {
	s, _ := ctx.Value(sessionKey{}).(session)
	return s
}
