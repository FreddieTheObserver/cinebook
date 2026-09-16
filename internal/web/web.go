package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

//go:embed static
var static embed.FS

// Nothing is inline and nothing comes from another origin, so the policy can
// be this strict.
const contentSecurityPolicy = "default-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; form-action 'self'"

type asset struct {
	name    string
	content []byte
	etag    string
}

// Handler serves the browser UI. Its pages call /v1 on the same origin, which
// spares the API a CORS layer for the custom X-Customer-Ref header.
func Handler() http.Handler {
	assets := loadAssets()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		a, ok := assets[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}

		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		// Hold tokens and booking references live in the URL fragment, which is
		// never sent anyway; this also keeps the page URL itself private.
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		// Revalidated on every load, so pages from a new binary never run
		// against a script cached from the old one.
		h.Set("Cache-Control", "no-cache")
		h.Set("ETag", a.etag)
		http.ServeContent(w, r, a.name, time.Time{}, bytes.NewReader(a.content))
	})
}

func loadAssets() map[string]asset {
	assets := map[string]asset{}
	err := fs.WalkDir(static, "static", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := static.ReadFile(name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(content)
		assets[strings.TrimPrefix(name, "static")] = asset{
			name:    name,
			content: content,
			etag:    `"` + base64.RawURLEncoding.EncodeToString(sum[:18]) + `"`,
		}
		return nil
	})
	if err != nil {
		panic(fmt.Sprintf("web: embedded assets: %v", err))
	}

	assets["/"] = assets["/index.html"]
	delete(assets, "/index.html")
	return assets
}
