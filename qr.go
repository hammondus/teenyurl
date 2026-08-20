package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"net/http"
	"path"
	"strconv"
	"strings"

	"rsc.io/qr"
)

// quietZone is the blank margin around a QR code, in modules. The
// specification requires at least four; without it a scanner cannot find the
// code against a busy background.
const quietZone = 4

// QR image size in pixels, for the PNG form.
const (
	defaultQRSize = 512
	minQRSize     = 64
	maxQRSize     = 2048
)

// handleQR renders one link's short URL as a QR code.
//
// Only qr.Encode and Code.Black come from the library. Both images are drawn
// here, so the quiet zone and the pixel size are the same in each form rather
// than whatever the library's own PNG writer happens to do.
func (s *server) handleQR(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	ext := strings.ToLower(path.Ext(file))
	code := strings.TrimSuffix(file, path.Ext(file))
	if ext != ".png" && ext != ".svg" {
		http.NotFound(w, r)
		return
	}
	l, ok := s.store.Get(code)
	if !ok {
		http.NotFound(w, r)
		return
	}

	// Medium correction recovers about 15% of a damaged code, which is the
	// usual choice for a printed URL.
	c, err := qr.Encode(s.cfg.baseURL+"/"+l.Code, qr.M)
	if err != nil {
		log.Printf("qr: encode %s: %v", l.Code, err)
		http.Error(w, "could not draw the code", http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if ext == ".svg" {
		writeQRSVG(&buf, c)
		w.Header().Set("Content-Type", "image/svg+xml")
	} else {
		if err := writeQRPNG(&buf, c, qrSize(r.URL.Query().Get("size"))); err != nil {
			log.Printf("qr: encode png %s: %v", l.Code, err)
			http.Error(w, "could not draw the code", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", l.Code+ext))
	w.Write(buf.Bytes())
}

// qrSize reads the requested pixel width and clamps it.
func qrSize(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil {
		return defaultQRSize
	}
	return max(minQRSize, min(maxQRSize, n))
}

// writeQRPNG draws the code as a two-colour paletted image, which keeps the
// file small. The result is the largest whole number of pixels per module that
// fits inside want.
func writeQRPNG(w io.Writer, c *qr.Code, want int) error {
	modules := c.Size + 2*quietZone
	scale := max(1, want/modules)
	side := modules * scale

	img := image.NewPaletted(image.Rect(0, 0, side, side),
		color.Palette{color.White, color.Black})
	for y := range c.Size {
		for x := range c.Size {
			if !c.Black(x, y) {
				continue
			}
			left := (x + quietZone) * scale
			top := (y + quietZone) * scale
			for py := top; py < top+scale; py++ {
				for px := left; px < left+scale; px++ {
					img.SetColorIndex(px, py, 1)
				}
			}
		}
	}
	return png.Encode(w, img)
}

// writeQRSVG draws the code as one path of unit squares on a module grid, so
// it scales to any size without a pixel dimension baked in.
func writeQRSVG(w io.Writer, c *qr.Code) {
	side := c.Size + 2*quietZone
	var d strings.Builder
	for y := range c.Size {
		for x := range c.Size {
			if c.Black(x, y) {
				fmt.Fprintf(&d, "M%d %dh1v1h-1z", x+quietZone, y+quietZone)
			}
		}
	}
	fmt.Fprintf(w, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" `+
		`shape-rendering="crispEdges" role="img" aria-label="QR code">`+
		`<rect width="%d" height="%d" fill="#fff"/>`+
		`<path fill="#000" d="%s"/></svg>`+"\n",
		side, side, side, side, d.String())
}
