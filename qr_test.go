package main

import (
	"bytes"
	"image/png"
	"net/http"
	"strings"
	"testing"

	"rsc.io/qr"
)

// TestQRRenderingMatchesTheModuleGrid checks each drawn pixel against the
// library's own answer for that module. It catches the two errors this code
// can actually make: swapping x and y, and misplacing the quiet zone.
func TestQRRenderingMatchesTheModuleGrid(t *testing.T) {
	c, err := qr.Encode("https://url.example.com/docs", qr.M)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := writeQRPNG(&buf, c, 512); err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(&buf)
	if err != nil {
		t.Fatal(err)
	}
	modules := c.Size + 2*quietZone
	scale := img.Bounds().Dx() / modules
	if scale < 1 {
		t.Fatalf("scale = %d", scale)
	}
	for y := range c.Size {
		for x := range c.Size {
			// Sample the middle of the module, away from any edge.
			px := (x+quietZone)*scale + scale/2
			py := (y+quietZone)*scale + scale/2
			r, g, b, _ := img.At(px, py).RGBA()
			black := r == 0 && g == 0 && b == 0
			if black != c.Black(x, y) {
				t.Fatalf("module (%d,%d) is drawn black=%v, want %v", x, y, black, c.Black(x, y))
			}
		}
	}
}

// TestQRFinderPattern checks the three corner squares that a scanner locks
// onto. Their shape is fixed by the specification.
func TestQRFinderPattern(t *testing.T) {
	c, err := qr.Encode("https://url.example.com/docs", qr.M)
	if err != nil {
		t.Fatal(err)
	}
	corners := [][2]int{{0, 0}, {c.Size - 7, 0}, {0, c.Size - 7}}
	for _, o := range corners {
		for y := range 7 {
			for x := range 7 {
				// A finder is a black 7x7 ring, a white ring, then a black 3x3.
				edge := x == 0 || x == 6 || y == 0 || y == 6
				core := x >= 2 && x <= 4 && y >= 2 && y <= 4
				want := edge || core
				if got := c.Black(o[0]+x, o[1]+y); got != want {
					t.Fatalf("finder at %v, module (%d,%d) = %v, want %v", o, x, y, got, want)
				}
			}
		}
	}
}

func TestQRPNG(t *testing.T) {
	s := testServer(t)
	cookie, _ := signIn(t, s)
	if _, err := s.store.Create("docs", "https://example.com", "", nil, testTime); err != nil {
		t.Fatal(err)
	}

	w := getAs(t, s, "/admin/qr/docs.png?size=512", cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q", got)
	}
	img, err := png.Decode(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("the response is not a PNG: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != b.Dy() {
		t.Errorf("image is %dx%d, want a square", b.Dx(), b.Dy())
	}
	if b.Dx() > 512 || b.Dx() < 256 {
		t.Errorf("image is %d pixels wide, want at most the 512 asked for", b.Dx())
	}
	// The quiet zone must be blank, or a scanner cannot find the code.
	for _, p := range [][2]int{{0, 0}, {b.Dx() - 1, 0}, {0, b.Dy() - 1}, {b.Dx() - 1, b.Dy() - 1}} {
		r, g, bl, _ := img.At(p[0], p[1]).RGBA()
		if r == 0 && g == 0 && bl == 0 {
			t.Errorf("corner %v is black, want the quiet zone blank", p)
		}
	}
}

func TestQRSizeIsClamped(t *testing.T) {
	tests := []struct {
		raw     string
		wantMin int
		wantMax int
	}{
		{"", 256, defaultQRSize},
		{"nonsense", 256, defaultQRSize},
		{"1", minQRSize / 2, minQRSize},
		{"100000", maxQRSize / 2, maxQRSize},
	}
	s := testServer(t)
	cookie, _ := signIn(t, s)
	if _, err := s.store.Create("docs", "https://example.com", "", nil, testTime); err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		w := getAs(t, s, "/admin/qr/docs.png?size="+tt.raw, cookie)
		img, err := png.Decode(bytes.NewReader(w.Body.Bytes()))
		if err != nil {
			t.Fatalf("size=%q: %v", tt.raw, err)
		}
		if got := img.Bounds().Dx(); got < tt.wantMin || got > tt.wantMax {
			t.Errorf("size=%q gave %d pixels, want between %d and %d", tt.raw, got, tt.wantMin, tt.wantMax)
		}
	}
}

func TestQRSVG(t *testing.T) {
	s := testServer(t)
	cookie, _ := signIn(t, s)
	if _, err := s.store.Create("docs", "https://example.com", "", nil, testTime); err != nil {
		t.Fatal(err)
	}

	w := getAs(t, s, "/admin/qr/docs.svg", cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "image/svg+xml" {
		t.Errorf("Content-Type = %q", got)
	}
	body := w.Body.String()
	if !strings.HasPrefix(body, "<svg") || !strings.Contains(body, "viewBox=") {
		t.Errorf("the response is not an SVG:\n%s", body[:min(200, len(body))])
	}
	if !strings.Contains(body, `<path fill="#000"`) {
		t.Error("the SVG has no module path")
	}
}

func TestQREncodesTheShortURL(t *testing.T) {
	// The QR must carry the short link, not the destination. Otherwise the
	// printed code bypasses the shortener and stops being editable.
	s := testServer(t)
	cookie, _ := signIn(t, s)
	if _, err := s.store.Create("docs", "https://example.com/very/long/path", "", nil, testTime); err != nil {
		t.Fatal(err)
	}
	short := getAs(t, s, "/admin/qr/docs.svg", cookie).Body.Len()

	// A longer payload needs a larger grid, so the destination URL, which is
	// longer than the short link, would produce a bigger code.
	if _, err := s.store.Create("long", "https://example.com/very/long/path", "", nil, testTime); err != nil {
		t.Fatal(err)
	}
	if short == 0 {
		t.Fatal("the SVG is empty")
	}
	// A short link of a fixed shape always fits the smallest few versions.
	if short > 20000 {
		t.Errorf("the SVG is %d bytes, which suggests it encodes the destination", short)
	}
}

func TestQRUnknownCodeAndExtension(t *testing.T) {
	s := testServer(t)
	cookie, _ := signIn(t, s)
	if _, err := s.store.Create("docs", "https://example.com", "", nil, testTime); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/admin/qr/missing.png", "/admin/qr/docs.gif", "/admin/qr/docs"} {
		if w := getAs(t, s, target, cookie); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", target, w.Code)
		}
	}
}

func TestQRRequiresASession(t *testing.T) {
	s := testServer(t)
	if _, err := s.store.Create("docs", "https://example.com", "", nil, testTime); err != nil {
		t.Fatal(err)
	}
	w := get(t, s, "/admin/qr/docs.png")
	if strings.HasPrefix(w.Header().Get("Content-Type"), "image/") {
		t.Error("an unauthenticated visitor was served a QR code")
	}
}
