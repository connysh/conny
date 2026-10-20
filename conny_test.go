package conny

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/grpcreflect/v2"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestResolveTarget(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		wantURL string
		wantH2C bool
		wantErr bool
	}{
		{"http", "http://localhost:8080", "http://localhost:8080", false, false},
		{"https", "https://api.example.com", "https://api.example.com", false, false},
		{"h2c rewritten", "h2c://localhost:8080", "http://localhost:8080", true, false},
		{"empty", "", "", false, true},
		{"invalid", "http://bad url with spaces", "", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Config{Target: tt.target}
			u, h2c, err := c.resolveTarget()
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if u.String() != tt.wantURL {
				t.Errorf("url = %q, want %q", u.String(), tt.wantURL)
			}
			if h2c != tt.wantH2C {
				t.Errorf("h2c = %v, want %v", h2c, tt.wantH2C)
			}
		})
	}
}

func TestParseProtocol(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", connect.ProtocolNameConnect, false},
		{"connect", connect.ProtocolNameConnect, false},
		{"grpc", connect.ProtocolNameGRPC, false},
		{"grpcweb", connect.ProtocolNameGRPCWeb, false},
		{"grpc-web", connect.ProtocolNameGRPCWeb, false},
		{"http", "", true},
	}
	for _, tt := range tests {
		got, err := parseProtocol(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseProtocol(%q): expected error, got nil", tt.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseProtocol(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("parseProtocol(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestResolveDescriptorRequired(t *testing.T) {
	c := Config{}
	if _, err := c.resolveDescriptor(); err == nil {
		t.Fatal("expected error for missing descriptor, got nil")
	}
}

func TestNewHandlerValidation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{"missing descriptor", Config{Target: "http://localhost:8080"}, "Descriptor or DescriptorPath"},
		{"missing target", Config{DescriptorPath: "testdata/nope.pb"}, ""},
		{"bad descriptor path", Config{DescriptorPath: "testdata/nope.pb", Target: "http://localhost:8080"}, "reading descriptor file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewHandler(tt.cfg)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// These tests run the whole gateway against a real upstream, once per upstream
// protocol, and call it the way each kind of client would.

func TestGateway(t *testing.T) {
	for _, protocol := range []string{"connect", "grpc", "grpcweb"} {
		t.Run(protocol, func(t *testing.T) {
			gateway := startGateway(t, Config{Protocol: protocol, Reflection: true, MPP: true})

			t.Run("connect json", func(t *testing.T) {
				request, err := http.NewRequest(http.MethodPost, gateway+"/test.v1.TestService/DoThing",
					strings.NewReader(`{"name":"thing_1","unknown":true}`))
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("X-Request", "hello")
				response := do(t, request)
				if response.status != http.StatusOK || response.body != `{"id":"thing_1"}` {
					t.Errorf("response = %d %s, want 200 {\"id\":\"thing_1\"}", response.status, response.body)
				}
				if got := response.header.Get("X-Echo"); got != "hello" {
					t.Errorf("X-Echo = %q, want the request header echoed", got)
				}
			})

			t.Run("rest", func(t *testing.T) {
				request, err := http.NewRequest(http.MethodGet, gateway+"/v1/things?name=thing_1&extra=ignored", nil)
				if err != nil {
					t.Fatal(err)
				}
				response := do(t, request)
				if response.status != http.StatusOK || response.body != `{"id":"thing_1"}` {
					t.Errorf("response = %d %s, want 200 {\"id\":\"thing_1\"}", response.status, response.body)
				}
			})

			t.Run("rest payment required", func(t *testing.T) {
				request, err := http.NewRequest(http.MethodGet, gateway+"/v1/things?name=pay", nil)
				if err != nil {
					t.Fatal(err)
				}
				response := do(t, request)
				if response.status != http.StatusPaymentRequired {
					t.Errorf("status = %d, want 402: %s", response.status, response.body)
				}
				if got := response.header.Get("Www-Authenticate"); got != specChallenge {
					t.Errorf("Www-Authenticate = %q, want the upstream's challenge", got)
				}
			})

			for _, clientProtocol := range []connecthttp.Option{connecthttp.WithGRPC(), connecthttp.WithGRPCWeb()} {
				client := gatewayClient(gateway, clientProtocol)

				t.Run("unary", func(t *testing.T) {
					ctx, info := connect.NewClientContext(t.Context())
					info.RequestHeader().Set("X-Request", "hello")
					response := dynamicpb.NewMessage(testMessage(t, "test.v1.Response"))
					err := client.CallUnary(ctx, testSpec(t, "test.v1.TestService.DoThing"),
						testRequest(t, "thing_1"), response)
					if err != nil {
						t.Fatal(err)
					}
					if got := stringField(response, "id"); got != "thing_1" {
						t.Errorf("id = %q, want thing_1", got)
					}
					if got := info.ResponseHeader().Get("X-Echo"); got != "hello" {
						t.Errorf("X-Echo = %q, want the request header echoed", got)
					}
				})

				t.Run("error", func(t *testing.T) {
					response := dynamicpb.NewMessage(testMessage(t, "test.v1.Response"))
					err := client.CallUnary(t.Context(), testSpec(t, "test.v1.TestService.DoThing"),
						testRequest(t, "missing"), response)
					if code := connect.CodeOf(err); code != connect.CodeNotFound {
						t.Errorf("code = %v, want not_found: %v", code, err)
					}
				})

				t.Run("server stream", func(t *testing.T) {
					stream, err := client.CallServerStream(t.Context(), testSpec(t, "test.v1.TestService.WatchThings"),
						testRequest(t, "thing"))
					if err != nil {
						t.Fatal(err)
					}
					defer stream.Close()
					var ids []string
					for {
						response := dynamicpb.NewMessage(testMessage(t, "test.v1.Response"))
						err := stream.Receive(response)
						if errors.Is(err, io.EOF) {
							break
						}
						if err != nil {
							t.Fatal(err)
						}
						ids = append(ids, stringField(response, "id"))
					}
					if want := []string{"thing_0", "thing_1"}; !slices.Equal(ids, want) {
						t.Errorf("ids = %v, want %v", ids, want)
					}
				})
			}

			t.Run("reflection", func(t *testing.T) {
				reflection := grpcreflect.NewClient(gatewayClient(gateway, connecthttp.WithGRPC()))
				stream := reflection.NewStream(t.Context())
				defer stream.Close()
				services, err := stream.ListServices()
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Contains(services, "test.v1.TestService") {
					t.Errorf("services = %v, want test.v1.TestService", services)
				}
				files, err := stream.FileContainingSymbol("test.v1.TestService")
				if err != nil {
					t.Fatal(err)
				}
				if len(files) == 0 || files[0].GetPackage() != "test.v1" {
					t.Errorf("files = %v, want test.v1's", files)
				}
			})
		})
	}
}

func TestGatewayUpstreamDown(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	target := upstream.URL
	upstream.Close()

	handler, err := NewHandler(Config{DescriptorPath: testDescriptorPath(t), Target: target, Logger: quietLogger()})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/test.v1.TestService/DoThing", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	var body struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)
	if body.Code != "unavailable" {
		t.Errorf("response = %d %s, want an unavailable error", recorder.Code, recorder.Body)
	}
}

// startGateway serves c in front of a test upstream, both over h2c, and
// returns the gateway's base URL.
func startGateway(t *testing.T, c Config) string {
	t.Helper()

	upstream := connect.NewServer()
	for _, name := range []string{"GetThing", "DoThing", "WatchThings"} {
		upstream.Register(connect.Method{
			Spec:    testSpec(t, "test.v1.TestService."+name),
			Handler: upstreamHandler(t),
		})
	}
	upstreamMux := http.NewServeMux()
	connecthttp.Mount(upstreamMux, upstream)
	c.Target = "h2c://" + strings.TrimPrefix(startH2C(t, upstreamMux), "http://")

	c.DescriptorPath = testDescriptorPath(t)
	c.Logger = quietLogger()
	handler, err := NewHandler(c)
	if err != nil {
		t.Fatal(err)
	}
	return startH2C(t, handler)
}

// upstreamHandler answers with the request's name as the id, echoes X-Request
// as X-Echo, demands payment for "pay", and has no "missing" thing.
func upstreamHandler(t *testing.T) connect.ServerFunc {
	return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
		request := dynamicpb.NewMessage(testMessage(t, "test.v1.Request"))
		if err := stream.Receive(request); err != nil {
			return err
		}
		info, _ := connect.CallInfoForServerContext(ctx)
		info.ResponseHeader().Set("X-Echo", info.RequestHeader().Get("X-Request"))

		name := stringField(request, "name")
		switch name {
		case "pay":
			info.ResponseHeader().Set("Www-Authenticate", specChallenge)
			return connect.NewError(connect.CodeUnauthenticated, "payment required")
		case "missing":
			return connect.NewError(connect.CodeNotFound, "no such thing")
		}

		names := []string{name}
		if spec.StreamType == connect.StreamTypeServer {
			names = []string{name + "_0", name + "_1"}
		}
		for _, id := range names {
			response := dynamicpb.NewMessage(testMessage(t, "test.v1.Response"))
			setStringField(response, "id", id)
			if err := stream.Send(response); err != nil {
				return err
			}
		}
		return nil
	}
}

func startH2C(t *testing.T, handler http.Handler) string {
	t.Helper()

	server := httptest.NewUnstartedServer(handler)
	server.Config.Protocols = new(http.Protocols)
	server.Config.Protocols.SetHTTP1(true)
	server.Config.Protocols.SetUnencryptedHTTP2(true)
	server.Start()
	t.Cleanup(server.Close)
	return server.URL
}

func gatewayClient(baseURL string, options ...connecthttp.Option) *connect.Client {
	transport := &http.Transport{Protocols: new(http.Protocols)}
	transport.Protocols.SetUnencryptedHTTP2(true)
	return connect.NewClient(connecthttp.NewTransport(&http.Client{Transport: transport}, baseURL, options...))
}

func testSpec(t *testing.T, name string) connect.Spec {
	t.Helper()

	md := testMethod(t, name)
	spec := connect.Spec{
		Schema:    md,
		Procedure: "/" + string(md.Parent().FullName()) + "/" + string(md.Name()),
	}
	if md.IsStreamingServer() {
		spec.StreamType = connect.StreamTypeServer
	}
	return spec
}

func testRequest(t *testing.T, name string) *dynamicpb.Message {
	t.Helper()

	request := dynamicpb.NewMessage(testMessage(t, "test.v1.Request"))
	setStringField(request, "name", name)
	return request
}

func stringField(message *dynamicpb.Message, name protoreflect.Name) string {
	return message.Get(message.Descriptor().Fields().ByName(name)).String()
}

func setStringField(message *dynamicpb.Message, name protoreflect.Name, value string) {
	message.Set(message.Descriptor().Fields().ByName(name), protoreflect.ValueOfString(value))
}

type response struct {
	status int
	header http.Header
	body   string
}

func do(t *testing.T, request *http.Request) response {
	t.Helper()

	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: strings.TrimSpace(string(body))}
}
