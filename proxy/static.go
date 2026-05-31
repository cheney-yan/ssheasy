package main

import (
	"mime"
	"net/http"
	"path"
	"strings"
)

func init() {
	// Serve the PWA manifest with the correct media type (the platform mime
	// table doesn't know .webmanifest, so it would default to text/plain).
	mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

// spaFileServer serves static files from root and falls back to /index.html
// for any path that doesn't resolve to a real file, so client-side routes
// (single-page app) reload correctly instead of 404ing.
func spaFileServer(root FileSystem) http.Handler {
	fs := FileServer(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upath := r.URL.Path
		if !strings.HasPrefix(upath, "/") {
			upath = "/" + upath
		}
		name := path.Clean(upath)

		if f, err := root.Open(name); err == nil {
			st, serr := f.Stat()
			f.Close()
			if serr == nil && (!st.IsDir() || name == "/") {
				fs.ServeHTTP(w, r) // real file, or root dir -> its index.html
				return
			}
		}

		// Unknown path: serve the SPA entrypoint.
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
