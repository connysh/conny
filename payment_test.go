package conny

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsPaymentChallenge(t *testing.T) {
	tests := []struct {
		header string
		want   bool
	}{
		{"Payment", true},
		{"payment", true},
		{"Payment realm=\"api\", amount=100", true},
		{"Bearer realm=\"api\"", false},
		{"Basic", false},
		{"", false},
		{"PaymentX", false},
	}
	for _, tt := range tests {
		if got := isPaymentChallenge(tt.header); got != tt.want {
			t.Errorf("isPaymentChallenge(%q) = %v, want %v", tt.header, got, tt.want)
		}
	}
}

func TestIsRPCRequest(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		connectHdr  string
		query       string
		want        bool
	}{
		{"rest json", "application/json", "", "", false},
		{"rest no content type", "", "", "", false},
		{"grpc", "application/grpc", "", "", true},
		{"grpc proto", "application/grpc+proto", "", "", true},
		{"grpc-web", "application/grpc-web+proto", "", "", true},
		{"connect streaming", "application/connect+json", "", "", true},
		{"connect unary header", "application/json", "1", "", true},
		{"connect get query", "", "", "connect=v1", true},
		{"wrong connect version", "application/json", "2", "", false},
		{"wrong connect query", "", "", "connect=v2", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/svc/Method?"+tt.query, nil)
			if tt.contentType != "" {
				r.Header.Set("Content-Type", tt.contentType)
			}
			if tt.connectHdr != "" {
				r.Header.Set("Connect-Protocol-Version", tt.connectHdr)
			}
			if got := isRPCRequest(r); got != tt.want {
				t.Errorf("isRPCRequest() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWithPaymentRequired(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		wwwAuth    string
		rpcRequest bool
		wantStatus int
	}{
		{"401 payment upgraded", http.StatusUnauthorized, "Payment realm=\"api\"", false, http.StatusPaymentRequired},
		{"401 bearer untouched", http.StatusUnauthorized, "Bearer realm=\"api\"", false, http.StatusUnauthorized},
		{"401 no challenge untouched", http.StatusUnauthorized, "", false, http.StatusUnauthorized},
		{"403 payment untouched", http.StatusForbidden, "Payment", false, http.StatusForbidden},
		{"200 untouched", http.StatusOK, "", false, http.StatusOK},
		{"rpc request untouched", http.StatusUnauthorized, "Payment", true, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.wwwAuth != "" {
					w.Header().Set("Www-Authenticate", tt.wwwAuth)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte("body"))
			})
			r := httptest.NewRequest(http.MethodGet, "/svc/Method", nil)
			if tt.rpcRequest {
				r.Header.Set("Connect-Protocol-Version", "1")
			}
			w := httptest.NewRecorder()

			withPaymentRequired(next).ServeHTTP(w, r)

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if got := w.Body.String(); got != "body" {
				t.Errorf("body = %q, want %q", got, "body")
			}
			if tt.wwwAuth != "" && w.Header().Get("Www-Authenticate") != tt.wwwAuth {
				t.Errorf("Www-Authenticate header not preserved")
			}
		})
	}
}

func TestPaymentRequiredWriterImplicitHeader(t *testing.T) {
	// Write without an explicit WriteHeader must default to 200.
	rec := httptest.NewRecorder()
	w := &paymentRequiredWriter{ResponseWriter: rec}
	if _, err := w.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestPaymentRequiredWriterOnlyFirstHeaderRewritten(t *testing.T) {
	// A second WriteHeader call (invalid, but defensive) must not re-evaluate.
	rec := httptest.NewRecorder()
	w := &paymentRequiredWriter{ResponseWriter: rec}
	w.Header().Set("Www-Authenticate", "Payment")
	w.WriteHeader(http.StatusOK)
	w.WriteHeader(http.StatusUnauthorized)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}
