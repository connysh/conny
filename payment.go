package conny

import (
	"net/http"
	"strings"
)

// isPaymentChallenge reports whether wwwAuthenticate uses the "Payment" scheme.
func isPaymentChallenge(wwwAuthenticate string) bool {
	scheme := wwwAuthenticate
	if i := strings.IndexByte(scheme, ' '); i >= 0 {
		scheme = scheme[:i]
	}
	return strings.EqualFold(scheme, "Payment")
}

// withPaymentRequired upgrades REST 401 responses carrying a "Payment"
// WWW-Authenticate challenge to 402 Payment Required. gRPC/Connect have no
// code for 402, so an upstream signals it as Unauthenticated plus the
// challenge header. RPC requests pass through untouched: their clients read
// the code from the body/trailer, and rewriting the HTTP status would only
// make the two disagree.
func withPaymentRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isRPCRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(&paymentRequiredWriter{ResponseWriter: w}, r)
	})
}

// isRPCRequest reports whether the request arrived over gRPC, gRPC-Web, or
// Connect rather than as a plain REST/HTTP call. The Connect markers match
// vanguard's classifyRequest; they are the only thing distinguishing
// Connect-unary from REST, which share the application/json content type.
func isRPCRequest(r *http.Request) bool {
	if r.Header.Get("Connect-Protocol-Version") == "1" || r.URL.Query().Get("connect") == "v1" {
		return true
	}
	contentType := r.Header.Get("Content-Type")
	return strings.HasPrefix(contentType, "application/grpc") ||
		strings.HasPrefix(contentType, "application/connect")
}

type paymentRequiredWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (w *paymentRequiredWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.wroteHeader = true
		if code == http.StatusUnauthorized &&
			isPaymentChallenge(w.Header().Get("Www-Authenticate")) {
			code = http.StatusPaymentRequired
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *paymentRequiredWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Flush and Unwrap expose the underlying writer's capabilities; the transcoder
// requires the response writer to implement http.Flusher.
func (w *paymentRequiredWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *paymentRequiredWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
