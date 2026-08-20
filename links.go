package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Link is one short link.
//
// A record in links.jsonl holds the complete current state of a link, never a
// change to one, so replaying the log is "last record wins per code". Deletion
// appends a record with Deleted set; without that tombstone, replay would
// resurrect the link from its earlier create record.
type Link struct {
	Code      string    `json:"code"`
	URL       string    `json:"url"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// omitzero, not omitempty: omitempty never skips a struct, so a zero
	// time.Time would be written into every create record.
	UpdatedAt time.Time  `json:"updated_at,omitzero"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Deleted   bool       `json:"deleted,omitempty"`
}

// Expired reports whether the link has passed its expiry time. A link with no
// expiry never expires.
func (l *Link) Expired(now time.Time) bool {
	return l.ExpiresAt != nil && !now.Before(*l.ExpiresAt)
}

// codeAlphabet is base62 minus 0, O, I, l, and 1. Short links carry QR codes,
// a QR code means a link gets read off paper, and a link read off paper gets
// typed by hand. Dropping the ambiguous glyphs costs five characters of
// alphabet and removes the most common transcription error.
const codeAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// codeRejectAbove is the largest multiple of len(codeAlphabet) below 256.
// randomCode discards any byte at or above it, because 256 is not a multiple
// of 57: a plain modulo would make the first 28 characters of the alphabet
// more likely than the rest.
const codeRejectAbove = 228

// defaultCodeLen gives 57^6, about 34 billion combinations. Against the
// hundreds of links this service is sized for, a random guess lands about once
// in 10^8 tries.
const defaultCodeLen = 6

// randomCode returns n characters drawn uniformly from codeAlphabet.
func randomCode(n int) (string, error) {
	if n < 1 {
		return "", fmt.Errorf("code length %d is below 1", n)
	}
	out := make([]byte, 0, n)
	// Read more bytes than needed, since roughly one in nine is rejected.
	buf := make([]byte, n+n/4+8)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("read random bytes: %w", err)
		}
		for _, b := range buf {
			if b >= codeRejectAbove {
				continue
			}
			out = append(out, codeAlphabet[int(b)%len(codeAlphabet)])
			if len(out) == n {
				break
			}
		}
	}
	return string(out), nil
}

// aliasPattern is what a hand-picked code may contain. It excludes the dot, so
// an alias can never shadow a file name such as robots.txt.
var aliasPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// reservedCodes are paths the server serves itself. Matching is
// case-insensitive, so neither /admin nor /Admin can be claimed.
var reservedCodes = map[string]bool{
	"admin":       true,
	"api":         true,
	"static":      true,
	"healthz":     true,
	"robots.txt":  true,
	"favicon.ico": true,
}

// validateCode checks a hand-picked alias. Generated codes always pass.
func validateCode(code string) error {
	if !aliasPattern.MatchString(code) {
		return errors.New("code must be 1 to 64 characters, using letters, digits, hyphen, or underscore")
	}
	if reservedCodes[strings.ToLower(code)] {
		return fmt.Errorf("code %q is reserved", code)
	}
	return nil
}

// validateURL checks a destination and returns it normalised.
//
// Rejecting every scheme but http and https matters because the preview page
// renders the destination inside an anchor element. An unvalidated
// javascript: target would be stored cross-site scripting.
func validateURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("URL is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("not a valid URL: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return "", errors.New("URL must start with http:// or https://")
	}
	if u.Host == "" {
		return "", errors.New("URL must include a host")
	}
	return u.String(), nil
}
