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
	appName         = "python3"
	brandedIndex    []byte
	brandedManifest []byte
)

func loadBranding(htmlDir string) {
	if v := strings.TrimSpace(os.Getenv("APP_NAME")); v != "" {
		appName = v
	}
	if b, err := os.ReadFile(filepath.Join(htmlDir, "index.html")); err == nil {
		brandedIndex = []byte(strings.ReplaceAll(string(b), "__APP_NAME__", appName))
	}
	if b, err := os.ReadFile(filepath.Join(htmlDir, "manifest.webmanifest")); err == nil {
		brandedManifest = []byte(strings.ReplaceAll(string(b), "__APP_NAME__", appName))
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

		if name == "/manifest.webmanifest" && brandedManifest != nil {
			w.Header().Set("Content-Type", "application/manifest+json")
			w.Write(brandedManifest)
			return
		}

		// Real on-disk file (but never the raw index.html — that's branded).
		if name != "/" && name != "/index.html" {
			if f, err := root.Open(name); err == nil {
				st, serr := f.Stat()
				f.Close()
				if serr == nil && !st.IsDir() {
					fs.ServeHTTP(w, r)
					return
				}
			}
		}

		// Branded SPA entrypoint for "/", "/index.html", and unknown paths.
		if brandedIndex != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(brandedIndex)
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
