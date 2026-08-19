package conny

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func staticTestHandler(t *testing.T) (http.Handler, string) {
	t.Helper()

	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("openapi.json", `{"openapi":"3.1.0"}`)
	write("index.html", "<h1>docs</h1>")
	write("docs/index.html", "<h1>nested</h1>")
	write(".env", "SECRET_KEY=hunter2")
	write(".git/HEAD", "ref: refs/heads/main")
	write("..data/openapi.json", `{"openapi":"3.1.0"}`)
	if err := os.MkdirAll(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Stands in for the transcoder so fallthrough is observable.
	fallthroughHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("upstream"))
	})

	h, err := withStatic(dir, fallthroughHandler)
	if err != nil {
		t.Fatal(err)
	}
	return h, dir
}

func TestStaticServesFiles(t *testing.T) {
	h, _ := staticTestHandler(t)

	tests := []struct {
		name string
		path string
		want string
	}{
		{"spec", "/openapi.json", `{"openapi":"3.1.0"}`},
		{"root index", "/", "<h1>docs</h1>"},
		{"nested index", "/docs/", "<h1>nested</h1>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Body.String(); got != tt.want {
				t.Errorf("body = %q, want %q", got, tt.want)
			}
		})
	}
}

// Anything the static tree does not hold must reach the transcoder, or the
// gateway would stop serving RPCs the moment --static was set.
func TestStaticFallsThrough(t *testing.T) {
	h, _ := staticTestHandler(t)

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"connect rpc path", http.MethodPost, "/pay.v1.PaymentService/GetTopup"},
		{"rest path", http.MethodGet, "/v1/sessions"},
		{"missing file", http.MethodGet, "/nope.json"},
		{"dir without index", http.MethodGet, "/empty/"},
		{"post to a real file", http.MethodPost, "/openapi.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
			if rec.Code != http.StatusTeapot || rec.Body.String() != "upstream" {
				t.Errorf("status = %d body = %q, want fallthrough to upstream", rec.Code, rec.Body.String())
			}
		})
	}
}

// http.FileServer serves dotfiles by default. A static dir will sometimes sit
// next to an .env or a .git, and Kubernetes volume internals live under
// ..data, so none of them may be reachable.
func TestStaticHidesDotfiles(t *testing.T) {
	h, _ := staticTestHandler(t)

	for _, p := range []string{"/.env", "/.git/HEAD", "/..data/openapi.json"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusTeapot {
			t.Errorf("GET %s = %d %q, want fallthrough", p, rec.Code, rec.Body.String())
		}
	}
}

func TestStaticNoTraversal(t *testing.T) {
	h, dir := staticTestHandler(t)

	secret := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(secret) })

	for _, p := range []string{"/../secret.txt", "/..%2fsecret.txt", "/docs/../../secret.txt"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if body := rec.Body.String(); body == "top secret" {
			t.Errorf("%s escaped the static dir", p)
		}
	}
}

func TestStaticMissingDirIsAnError(t *testing.T) {
	if _, err := withStatic(filepath.Join(t.TempDir(), "nope"), http.NotFoundHandler()); err == nil {
		t.Fatal("expected error for missing static dir, got nil")
	}
}

func TestStaticDirDisabledByDefault(t *testing.T) {
	if c := (Config{}); c.StaticDir != "" {
		t.Errorf("StaticDir = %q, want empty", c.StaticDir)
	}
}
