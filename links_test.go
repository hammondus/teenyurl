package main

import (
	"strings"
	"testing"
	"time"
)

func TestCodeAlphabet(t *testing.T) {
	if got, want := len(codeAlphabet), 57; got != want {
		t.Errorf("alphabet has %d characters, want %d", got, want)
	}
	if codeRejectAbove%len(codeAlphabet) != 0 {
		t.Errorf("codeRejectAbove %d is not a multiple of %d", codeRejectAbove, len(codeAlphabet))
	}
	if codeRejectAbove+len(codeAlphabet) <= 256 {
		t.Errorf("codeRejectAbove %d is not the largest multiple below 256", codeRejectAbove)
	}
	for _, c := range "0OIl1" {
		if strings.ContainsRune(codeAlphabet, c) {
			t.Errorf("alphabet contains the ambiguous character %q", c)
		}
	}
	seen := map[rune]bool{}
	for _, c := range codeAlphabet {
		if seen[c] {
			t.Errorf("alphabet repeats %q", c)
		}
		seen[c] = true
	}
}

func TestRandomCodeShapeAndSpread(t *testing.T) {
	const n = 6
	seen := map[string]bool{}
	used := map[rune]bool{}
	for i := 0; i < 5000; i++ {
		code, err := randomCode(n)
		if err != nil {
			t.Fatalf("randomCode: %v", err)
		}
		if len(code) != n {
			t.Fatalf("code %q has length %d, want %d", code, len(code), n)
		}
		for _, c := range code {
			if !strings.ContainsRune(codeAlphabet, c) {
				t.Fatalf("code %q contains %q, which is outside the alphabet", code, c)
			}
			used[c] = true
		}
		if seen[code] {
			t.Fatalf("randomCode repeated %q within 5000 draws", code)
		}
		seen[code] = true
	}
	// 30000 draws across 57 characters: every character should appear. A
	// character missing here means the rejection sampling dropped a range.
	if len(used) != len(codeAlphabet) {
		t.Errorf("only %d of %d alphabet characters appeared", len(used), len(codeAlphabet))
	}
}

func TestRandomCodeRejectsShortLength(t *testing.T) {
	if _, err := randomCode(0); err == nil {
		t.Error("randomCode(0) returned no error")
	}
}

func TestValidateCode(t *testing.T) {
	tests := []struct {
		code string
		ok   bool
	}{
		{"docs", true},
		{"Docs", true},
		{"a", true},
		{"my-link_2", true},
		{strings.Repeat("a", 64), true},
		{"", false},
		{strings.Repeat("a", 65), false},
		{"has space", false},
		{"has.dot", false},
		{"has/slash", false},
		{"admin", false},
		{"Admin", false},
		{"ADMIN", false},
		{"healthz", false},
		{"favicon.ico", false},
	}
	for _, tt := range tests {
		err := validateCode(tt.code)
		if tt.ok && err != nil {
			t.Errorf("validateCode(%q) = %v, want no error", tt.code, err)
		}
		if !tt.ok && err == nil {
			t.Errorf("validateCode(%q) returned no error, want one", tt.code)
		}
	}
}

func TestValidateURL(t *testing.T) {
	tests := []struct {
		raw  string
		ok   bool
		want string
	}{
		{"https://example.com", true, "https://example.com"},
		{"http://example.com/a?b=c#d", true, "http://example.com/a?b=c#d"},
		{"  https://example.com/x  ", true, "https://example.com/x"},
		// url.Parse lowercases the scheme, which is the normalisation wanted.
		{"HTTPS://example.com", true, "https://example.com"},
		{"", false, ""},
		{"example.com", false, ""},
		{"/relative/path", false, ""},
		{"javascript:alert(1)", false, ""},
		{"JavaScript:alert(1)", false, ""},
		{"data:text/html,<script>alert(1)</script>", false, ""},
		{"file:///etc/passwd", false, ""},
		{"ftp://example.com", false, ""},
		{"https://", false, ""},
	}
	for _, tt := range tests {
		got, err := validateURL(tt.raw)
		if tt.ok {
			if err != nil {
				t.Errorf("validateURL(%q) = %v, want no error", tt.raw, err)
				continue
			}
			if got != tt.want {
				t.Errorf("validateURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
			continue
		}
		if err == nil {
			t.Errorf("validateURL(%q) returned no error, want one", tt.raw)
		}
	}
}

func TestLinkExpired(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	if (&Link{}).Expired(now) {
		t.Error("a link with no expiry reports as expired")
	}
	if !(&Link{ExpiresAt: &past}).Expired(now) {
		t.Error("a link that expired an hour ago reports as live")
	}
	if (&Link{ExpiresAt: &future}).Expired(now) {
		t.Error("a link expiring in an hour reports as expired")
	}
	if !(&Link{ExpiresAt: &now}).Expired(now) {
		t.Error("expiry is exclusive at the boundary, want inclusive")
	}
}
