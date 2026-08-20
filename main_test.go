package main

import (
	"testing"
	"time"
)

func TestLoadConfigDefaults(t *testing.T) {
	c, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if c.addr != ":8080" {
		t.Errorf("addr = %q", c.addr)
	}
	if c.codeLen != defaultCodeLen {
		t.Errorf("codeLen = %d, want %d", c.codeLen, defaultCodeLen)
	}
	if c.flushInterval != 30*time.Second {
		t.Errorf("flushInterval = %v", c.flushInterval)
	}
}

func TestLoadConfigFromEnvironment(t *testing.T) {
	t.Setenv("TEENYURL_ADDR", "127.0.0.1:9000")
	t.Setenv("TEENYURL_BASE_URL", "https://url.hammond.zone/")
	t.Setenv("TEENYURL_FLUSH_INTERVAL", "5s")
	t.Setenv("TEENYURL_CODE_LEN", "8")

	c, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if c.addr != "127.0.0.1:9000" {
		t.Errorf("addr = %q", c.addr)
	}
	// A trailing slash would produce short links with a double slash.
	if c.baseURL != "https://url.hammond.zone" {
		t.Errorf("baseURL = %q, want the trailing slash trimmed", c.baseURL)
	}
	if c.host() != "url.hammond.zone" {
		t.Errorf("host = %q", c.host())
	}
	if c.flushInterval != 5*time.Second || c.codeLen != 8 {
		t.Errorf("flushInterval = %v, codeLen = %d", c.flushInterval, c.codeLen)
	}
}

func TestLoadConfigRejectsBadValues(t *testing.T) {
	tests := []struct{ key, value string }{
		{"TEENYURL_BASE_URL", "url.hammond.zone"},
		{"TEENYURL_BASE_URL", "/relative"},
		{"TEENYURL_FLUSH_INTERVAL", "soon"},
		{"TEENYURL_FLUSH_INTERVAL", "0s"},
		{"TEENYURL_FLUSH_INTERVAL", "-5s"},
		{"TEENYURL_CODE_LEN", "many"},
		{"TEENYURL_CODE_LEN", "0"},
		{"TEENYURL_CODE_LEN", "65"},
	}
	for _, tt := range tests {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			t.Setenv(tt.key, tt.value)
			if _, err := loadConfig(); err == nil {
				t.Errorf("loadConfig accepted %s=%q", tt.key, tt.value)
			}
		})
	}
}
