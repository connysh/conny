package conny

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

// withStatic serves files from dir, falling through to next for any request
// that does not name a file. This lets a pre-generated openapi.json — or a
// docs UI, favicon, .well-known file — sit alongside the RPC routes without
// conny having to generate anything itself.
//
// Requests are confined to dir by [os.Root]; dot-prefixed names (.env, .git,
// and Kubernetes' ..data volume internals) are never served; and a directory
// without an index.html falls through rather than producing a listing.
func withStatic(dir string, next http.Handler) (http.Handler, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("conny: opening static dir: %w", err)
	}
	rootFS := root.FS()
	files := http.FileServerFS(rootFS)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		if !staticFileExists(rootFS, r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if ctype, ok := staticContentTypes[strings.ToLower(path.Ext(r.URL.Path))]; ok {
			w.Header().Set("Content-Type", ctype)
		}
		files.ServeHTTP(w, r)
	}), nil
}

var staticContentTypes = map[string]string{
	".yaml": "application/yaml", // RFC 9512
	".yml":  "application/yaml",
	".md":   "text/markdown; charset=utf-8", // RFC 7763
	".xml":  "application/xml",              // RFC 7303
}

// staticFileExists reports whether urlPath resolves to a regular file in fsys.
// Directories resolve through their index.html, matching http.FileServer.
func staticFileExists(fsys fs.FS, urlPath string) bool {
	name := strings.TrimPrefix(path.Clean("/"+urlPath), "/")
	if name == "" || name == "." {
		name = "index.html"
	}
	if !fs.ValidPath(name) || hasDotPrefixedElement(name) {
		return false
	}

	info, err := fs.Stat(fsys, name)
	if err != nil {
		return false
	}
	if info.IsDir() {
		index, err := fs.Stat(fsys, path.Join(name, "index.html"))
		return err == nil && index.Mode().IsRegular()
	}
	return info.Mode().IsRegular()
}

// hasDotPrefixedElement reports whether any path element starts with a dot.
// http.FileServer would happily serve .env or .git/HEAD, so screen them out
// before touching the filesystem.
func hasDotPrefixedElement(name string) bool {
	for elem := range strings.SplitSeq(name, "/") {
		if strings.HasPrefix(elem, ".") {
			return true
		}
	}
	return false
}
