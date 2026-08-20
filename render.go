package main

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"
)

//go:embed web/templates
var templateFS embed.FS

//go:embed web/static
var staticFS embed.FS

// repoURL is the public source, linked from the landing page.
const repoURL = "https://github.com/hammondus/teenyurl"

// page carries the fields base.html needs. Every page's data embeds it.
// Styles and Scripts name extra assets, so the public pages do not carry the
// admin stylesheet and the admin script.
type page struct {
	Title   string
	NoIndex bool
	Styles  []string
	Scripts []string
}

// renderer holds one parsed template per page, plus the static assets those
// pages reference.
type renderer struct {
	pages  map[string]*template.Template
	assets *assetServer
}

func newRenderer() (*renderer, error) {
	assets, err := newAssetServer()
	if err != nil {
		return nil, err
	}
	names, err := fs.Glob(templateFS, "web/templates/*.html")
	if err != nil {
		return nil, err
	}
	funcs := template.FuncMap{
		"asset":   assets.url,
		"iso":     isoTime,
		"dtvalue": dtValue,
	}
	rn := &renderer{pages: make(map[string]*template.Template), assets: assets}
	for _, name := range names {
		base := path.Base(name)
		if base == "base.html" {
			continue
		}
		// Parse each page with base.html separately. Templates share one
		// namespace, so parsing them all together would leave every page
		// fighting over the name "content".
		t, err := template.New("base.html").Funcs(funcs).ParseFS(templateFS, "web/templates/base.html", name)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		rn.pages[strings.TrimSuffix(base, ".html")] = t
	}
	if len(rn.pages) == 0 {
		return nil, fmt.Errorf("no page templates found")
	}
	return rn, nil
}

// render writes one page.
//
// The default for HTML is no-cache: store, but always revalidate. A page
// carries the URLs of every asset it references, so serving it stale defeats
// asset cache busting. A handler that already set a stronger policy keeps it.
func (rn *renderer) render(w http.ResponseWriter, status int, name string, data any) {
	t, ok := rn.pages[name]
	if !ok {
		log.Printf("render: no template named %q", name)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Render to a buffer first. A template error part-way through must not
	// leave a half-written 200 on the wire.
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "base.html", data); err != nil {
		log.Printf("render %s: %v", name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	if h.Get("Cache-Control") == "" {
		h.Set("Cache-Control", "no-cache")
	}
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

// asTime accepts either form a template might hold a timestamp in.
func asTime(v any) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t, !t.IsZero()
	case *time.Time:
		if t == nil {
			return time.Time{}, false
		}
		return *t, !t.IsZero()
	}
	return time.Time{}, false
}

// isoTime fills a <time> element's datetime attribute. Everything is stored
// and rendered as UTC; the page's JavaScript rewrites the visible text into
// the reader's own zone, which keeps tzdata out of the container image.
func isoTime(v any) string {
	t, ok := asTime(v)
	if !ok {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// dtvalue fills a datetime-local input, which takes no zone and no seconds.
func dtValue(v any) string {
	t, ok := asTime(v)
	if !ok {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04")
}

// staticFile is one embedded asset and its content hash.
type staticFile struct {
	data  []byte
	hash  string
	ctype string
}

// assetServer serves web/static and names each file by its content hash.
//
// Hashing once at startup is correct here specifically because the files are
// embedded in the binary: they cannot change while the process runs. Assets
// read from disk at runtime would need a hash per request.
type assetServer struct {
	files map[string]*staticFile
}

func newAssetServer() (*assetServer, error) {
	sub, err := fs.Sub(staticFS, "web/static")
	if err != nil {
		return nil, err
	}
	a := &assetServer{files: make(map[string]*staticFile)}
	err = fs.WalkDir(sub, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(sub, name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		ctype := mime.TypeByExtension(filepath.Ext(name))
		if ctype == "" {
			ctype = "application/octet-stream"
		}
		a.files[name] = &staticFile{data: b, hash: hex.EncodeToString(sum[:])[:12], ctype: ctype}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return a, nil
}

// url returns the cache-busting path for an asset, for use in templates.
func (a *assetServer) url(name string) string {
	f, ok := a.files[name]
	if !ok {
		log.Printf("assets: template references missing file %q", name)
		return "/static/" + name
	}
	return "/static/" + name + "?v=" + f.hash
}

func (a *assetServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/static/")
	f, ok := a.files[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", f.ctype)
	h.Set("ETag", `"`+f.hash+`"`)
	h.Set("X-Content-Type-Options", "nosniff")
	if r.URL.Query().Get("v") == f.hash {
		// The URL names the bytes, so it can never go stale.
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		// A bookmarked unversioned path can change under the client.
		h.Set("Cache-Control", "public, max-age=3600")
	}
	// The zero mod time keeps Last-Modified out; the ETag above drives
	// revalidation, and embedded files have no meaningful timestamp anyway.
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(f.data))
}
