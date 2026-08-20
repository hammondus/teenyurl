package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseTrustedProxies(t *testing.T) {
	p, err := parseTrustedProxies(" 10.0.0.0/8 , 127.0.0.1/32 ,, ::1/128 ")
	if err != nil {
		t.Fatalf("parseTrustedProxies: %v", err)
	}
	if len(p.nets) != 3 {
		t.Errorf("parsed %d prefixes, want 3", len(p.nets))
	}
	if _, err := parseTrustedProxies("10.0.0.1"); err == nil {
		t.Error("a bare address was accepted, want an error naming the field")
	}
	if _, err := parseTrustedProxies("not-a-cidr"); err == nil {
		t.Error("rubbish was accepted, want an error")
	}
}

func TestParseTrustedProxiesMasksHostBits(t *testing.T) {
	// 10.1.2.3/8 names the block 10.0.0.0/8. Without masking, Contains would
	// reject every address in it.
	p, err := parseTrustedProxies("10.1.2.3/8")
	if err != nil {
		t.Fatalf("parseTrustedProxies: %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.9.9.9:1234"
	if !p.trusted(peerAddr(r)) {
		t.Error("10.9.9.9 is not trusted under 10.1.2.3/8")
	}
}

func TestClientIP(t *testing.T) {
	p, err := parseTrustedProxies("127.0.0.1/32,172.16.0.0/12,::1/128")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		remote string
		xff    []string
		want   string
	}{
		{
			name:   "no proxy header",
			remote: "203.0.113.7:5000",
			want:   "203.0.113.7",
		},
		{
			name:   "untrusted peer forging the header is ignored",
			remote: "203.0.113.7:5000",
			xff:    []string{"10.0.0.1"},
			want:   "203.0.113.7",
		},
		{
			name:   "trusted proxy reports the client",
			remote: "172.18.0.2:5000",
			xff:    []string{"203.0.113.7"},
			want:   "203.0.113.7",
		},
		{
			name:   "chain is walked right to left past trusted hops",
			remote: "172.18.0.2:5000",
			xff:    []string{"203.0.113.7, 172.18.0.5, 172.18.0.9"},
			want:   "203.0.113.7",
		},
		{
			name:   "client-supplied prefix before the real client is ignored",
			remote: "172.18.0.2:5000",
			xff:    []string{"1.2.3.4, 203.0.113.7, 172.18.0.5"},
			want:   "203.0.113.7",
		},
		{
			name:   "several header lines are one chain",
			remote: "172.18.0.2:5000",
			xff:    []string{"203.0.113.7", "172.18.0.5"},
			want:   "203.0.113.7",
		},
		{
			name:   "malformed hop stops the walk at the peer",
			remote: "172.18.0.2:5000",
			xff:    []string{"203.0.113.7, garbage, 172.18.0.5"},
			want:   "172.18.0.2",
		},
		{
			name:   "trusted peer with no header stays the peer",
			remote: "172.18.0.2:5000",
			want:   "172.18.0.2",
		},
		{
			name:   "chain of only trusted hops falls back to the peer",
			remote: "172.18.0.2:5000",
			xff:    []string{"172.18.0.5, 172.18.0.9"},
			want:   "172.18.0.2",
		},
		{
			name:   "IPv6 loopback peer",
			remote: "[::1]:5000",
			xff:    []string{"2001:db8::1"},
			want:   "2001:db8::1",
		},
		{
			name:   "IPv4-mapped IPv6 is unmapped",
			remote: "172.18.0.2:5000",
			xff:    []string{"::ffff:203.0.113.7"},
			want:   "203.0.113.7",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tt.remote
			for _, v := range tt.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if got := p.clientIP(r).String(); got != tt.want {
				t.Errorf("clientIP = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestClientIPWithUnparseableRemoteAddr(t *testing.T) {
	p, _ := parseTrustedProxies("127.0.0.1/32")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "pipe"
	if got := p.clientIP(r); got.IsValid() {
		t.Errorf("clientIP = %v, want the zero address", got)
	}
}
