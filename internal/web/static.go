// Adapted from andon internal/web/routes_static.go (468474a).

package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"sync"
)

//go:embed static
var staticFiles embed.FS

const (
	assetVersionLen = 10
	cacheForever    = "public, max-age=31536000, immutable"
)

func (s *Server) registerStatic() {
	s.mux.Handle("GET /static/", cacheStatic(http.FileServerFS(staticFiles)))
}

// assetVersion is a hash over all static files: it changes with every
// build that touches them, e.g. /static/jidoka.css?v=3f2a9c01d4.
var assetVersion = sync.OnceValue(func() string {
	h := sha256.New()
	_ = fs.WalkDir(staticFiles, "static", func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}

		body, err := staticFiles.ReadFile(path)
		if err != nil {
			return err
		}
		h.Write([]byte(path))
		h.Write(body)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:assetVersionLen]
})

// asset is the template func for a static file's versioned URL.
func asset(name string) string {
	return "/static/" + name + "?v=" + assetVersion()
}

// cacheStatic lets browsers keep versioned files for good; plain URLs
// (fonts linked from CSS) revalidate every time.
func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("v") {
			w.Header().Set("Cache-Control", cacheForever)
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}
