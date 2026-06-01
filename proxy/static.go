package main

import (
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func init() {
	// Serve the PWA manifest with the correct media type (the platform mime
	// table doesn't know .webmanifest, so it would default to text/plain).
	mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

// White-label brand. The displayed app name is set at deploy time via APP_NAME
// (default "python3"); index.html and the manifest carry an __APP_NAME__
// placeholder that is substituted once at startup.
var (
	appName      = "python3"
	brandedFiles = map[string][]byte{} // "/path" -> APP_NAME-substituted content
)

func loadBranding(htmlDir string) {
	if v := strings.TrimSpace(os.Getenv("APP_NAME")); v != "" {
		appName = v
	}
	for _, f := range []string{"index.html", "manifest.webmanifest", "webauthn.html"} {
		if b, err := os.ReadFile(filepath.Join(htmlDir, f)); err == nil {
			brandedFiles["/"+f] = []byte(strings.ReplaceAll(string(b), "__APP_NAME__", appName))
		}
	}
}

// spaFileServer serves static files from root. index.html and the manifest are
// served branded (APP_NAME substituted); any unresolved path falls back to the
// branded index.html so client-side routes reload correctly.
func spaFileServer(root FileSystem) http.Handler {
	fs := FileServer(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upath := r.URL.Path
		if !strings.HasPrefix(upath, "/") {
			upath = "/" + upath
		}
		name := path.Clean(upath)
		if name == "/" {
			name = "/index.html"
		}

		// Branded files (index.html, manifest, webauthn.html) — APP_NAME applied.
		if b, ok := brandedFiles[name]; ok {
			if strings.HasSuffix(name, ".webmanifest") {
				w.Header().Set("Content-Type", "application/manifest+json")
			} else {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
			}
			w.Write(b)
			return
		}

		// Real on-disk file (index.html is always branded, handled above).
		if f, err := root.Open(name); err == nil {
			st, serr := f.Stat()
			f.Close()
			if serr == nil && !st.IsDir() {
				fs.ServeHTTP(w, r)
				return
			}
		}

		// Unknown path: fall back to the branded SPA entrypoint.
		if b, ok := brandedFiles["/index.html"]; ok {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(b)
			return
		}
		// Fallback if branding didn't load: serve index.html straight from disk.
		index, err := root.Open("/index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer index.Close()
		st, err := index.Stat()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		ServeContent(w, r, "index.html", st.ModTime(), index)
	})
}
