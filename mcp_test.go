package conny

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestToolName(t *testing.T) {
	md := testMethod(t, "test.v1.TestService.GetThing")

	if got, want := toolName(md), "test_v1_TestService_GetThing"; got != want {
		t.Errorf("toolName = %q, want %q", got, want)
	}
}

func TestToolNameOverLimitIsSkipped(t *testing.T) {
	longPackage := strings.Repeat("averylongsegment.", 8) + "v1"
	files := longNameFiles(t, longPackage)

	desc, err := files.FindDescriptorByName(protoreflect.FullName(
		longPackage + ".SubscriptionManagementService.ListSubscriptionEntitlements"))
	if err != nil {
		t.Fatal(err)
	}
	if name := toolName(desc.(protoreflect.MethodDescriptor)); len(name) <= maxToolNameLength {
		t.Fatalf("the fixture's name is %d bytes, too short to exercise the limit", len(name))
	}

	_, tools := newMCPHandler(files, http.NotFoundHandler(), "test", quietLogger())
	if tools != 0 {
		t.Errorf("tools = %d, want 0", tools)
	}
}

func longNameFiles(t *testing.T, packageName string) *protoregistry.Files {
	t.Helper()

	file := &descriptorpb.FileDescriptorProto{
		Name:        proto.String(strings.ReplaceAll(packageName, ".", "/") + "/long.proto"),
		Package:     proto.String(packageName),
		Syntax:      proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{Name: proto.String("Subscription")}},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("SubscriptionManagementService"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:       proto.String("ListSubscriptionEntitlements"),
				InputType:  proto.String("." + packageName + ".Subscription"),
				OutputType: proto.String("." + packageName + ".Subscription"),
			}},
		}},
	}
	files, err := protodesc.NewFiles(&descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{file},
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestMCPToolsList(t *testing.T) {
	handler, _ := mcpTestHandler(t)

	result := callMCP(t, handler, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)

	var tools struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			InputSchema struct {
				Type       string                     `json:"type"`
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"inputSchema"`
			Annotations struct {
				ReadOnlyHint bool `json:"readOnlyHint"`
			} `json:"annotations"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &tools); err != nil {
		t.Fatal(err)
	}

	byName := map[string]int{}
	for i, tool := range tools.Tools {
		byName[tool.Name] = i
	}

	if _, ok := byName["test_v1_TestService_WatchThings"]; ok {
		t.Error("streaming method was exposed as a tool")
	}
	if len(tools.Tools) != 2 {
		t.Fatalf("tools = %d, want 2: %v", len(tools.Tools), byName)
	}

	get, ok := byName["test_v1_TestService_GetThing"]
	if !ok {
		t.Fatalf("no GetThing tool: %v", byName)
	}
	if _, ok := byName["test_v1_TestService_DoThing"]; !ok {
		t.Fatalf("no DoThing tool: %v", byName)
	}

	wantDescription := "Fetches one thing.\nSecond line.\n\nCalls the test.v1.TestService.GetThing RPC."
	if got := tools.Tools[get].Description; got != wantDescription {
		t.Errorf("description = %q, want %q", got, wantDescription)
	}
	if got := tools.Tools[get].InputSchema.Type; got != "object" {
		t.Errorf("input schema type = %q, want object", got)
	}
	if _, ok := tools.Tools[get].InputSchema.Properties["name"]; !ok {
		t.Error("input schema is missing the request's name field")
	}

	if !tools.Tools[get].Annotations.ReadOnlyHint {
		t.Error("GetThing is bound to HTTP GET but was not marked read-only")
	}
	if tools.Tools[byName["test_v1_TestService_DoThing"]].Annotations.ReadOnlyHint {
		t.Error("DoThing was marked read-only without a GET binding")
	}
}

func TestMCPToolCall(t *testing.T) {
	handler, upstream := mcpTestHandler(t)
	upstream.body = `{"id":"thing_1","total":"9007199254740993"}`

	result := callMCP(t, handler, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
		`{"name":"test_v1_TestService_GetThing","arguments":{"name":"thing_1","total":"9007199254740993"}}}`)

	if got, want := upstream.request.URL.Path, "/test.v1.TestService/GetThing"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	if got, want := upstream.request.Method, http.MethodPost; got != want {
		t.Errorf("method = %q, want %q", got, want)
	}
	if got, want := upstream.request.Header.Get("Content-Type"), "application/json"; got != want {
		t.Errorf("content type = %q, want %q", got, want)
	}
	if got, want := upstream.request.Header.Get("Connect-Protocol-Version"), "1"; got != want {
		t.Errorf("connect protocol version = %q, want %q", got, want)
	}
	if got, want := upstream.requestBody, `{"name":"thing_1","total":"9007199254740993"}`; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}

	var call struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if err := json.Unmarshal(result, &call); err != nil {
		t.Fatal(err)
	}
	if call.IsError {
		t.Errorf("isError = true, want false: %s", result)
	}
	if len(call.Content) != 1 || call.Content[0].Text != upstream.body {
		t.Errorf("content = %v, want the response body %s", call.Content, upstream.body)
	}
	if got := string(call.StructuredContent); got != upstream.body {
		t.Errorf("structuredContent = %s, want %s", got, upstream.body)
	}
}

func TestMCPToolCallForwardsAuthorization(t *testing.T) {
	handler, upstream := mcpTestHandler(t)

	request := httptest.NewRequest(http.MethodPost, mcpPath, strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
			`{"name":"test_v1_TestService_GetThing","arguments":{}}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Authorization", "Bearer token-123")
	request.Header.Set("X-Secret", "not forwarded")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body)
	}

	if got, want := upstream.request.Header.Get("Authorization"), "Bearer token-123"; got != want {
		t.Errorf("authorization = %q, want %q", got, want)
	}
	if got := upstream.request.Header.Get("X-Secret"); got != "" {
		t.Errorf("x-secret = %q, want it dropped", got)
	}
}

func TestMCPToolCallWithoutArguments(t *testing.T) {
	handler, upstream := mcpTestHandler(t)

	callMCP(t, handler, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
		`{"name":"test_v1_TestService_GetThing"}}`)

	if got, want := upstream.requestBody, "{}"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestMCPToolCallUpstreamError(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{
			name:   "connect error",
			status: http.StatusNotFound,
			body:   `{"code":"not_found","message":"no such thing"}`,
			want:   "not_found: no such thing",
		},
		{
			name:   "code without message",
			status: http.StatusForbidden,
			body:   `{"code":"permission_denied"}`,
			want:   "permission_denied",
		},
		{
			name:   "no body",
			status: http.StatusBadGateway,
			want:   "upstream returned 502 Bad Gateway",
		},
		{
			name:   "unparseable body",
			status: http.StatusInternalServerError,
			body:   "upstream on fire",
			want:   "upstream returned 500 Internal Server Error: upstream on fire",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, upstream := mcpTestHandler(t)
			upstream.status = tt.status
			upstream.body = tt.body

			result := callMCP(t, handler, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
				`{"name":"test_v1_TestService_GetThing","arguments":{}}}`)

			var call struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			}
			if err := json.Unmarshal(result, &call); err != nil {
				t.Fatal(err)
			}
			if !call.IsError {
				t.Errorf("isError = false, want true: %s", result)
			}
			if len(call.Content) != 1 || call.Content[0].Text != tt.want {
				t.Errorf("content = %v, want %q", call.Content, tt.want)
			}
		})
	}
}

func TestMCPNoUnaryMethods(t *testing.T) {
	file := &descriptorpb.FileDescriptorProto{
		Name:        proto.String("stream/v1/stream.proto"),
		Package:     proto.String("stream.v1"),
		Syntax:      proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{Name: proto.String("Tick")}},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("StreamService"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:            proto.String("Watch"),
				InputType:       proto.String(".stream.v1.Tick"),
				OutputType:      proto.String(".stream.v1.Tick"),
				ServerStreaming: proto.Bool(true),
			}},
		}},
	}
	files, err := protodesc.NewFiles(&descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{file},
	})
	if err != nil {
		t.Fatal(err)
	}

	handler, tools := newMCPHandler(files, http.NotFoundHandler(), "test", quietLogger())
	if tools != 0 {
		t.Errorf("tools = %d, want 0", tools)
	}

	result := callMCP(t, handler, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var list struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(result, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 0 {
		t.Errorf("tools = %s, want none", result)
	}
}

func TestMCPServedByHandler(t *testing.T) {
	handler, err := NewHandler(Config{
		DescriptorPath: testDescriptorPath(t),
		Target:         "http://upstream.invalid",
		MCP:            true,
		Logger:         quietLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, mcpPath, strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), "test_v1_TestService_GetThing") {
		t.Errorf("body = %s, want it to list the service's tools", recorder.Body)
	}
}

func TestMCPDisabledByDefault(t *testing.T) {
	handler, err := NewHandler(Config{
		DescriptorPath: testDescriptorPath(t),
		Target:         "http://upstream.invalid",
		Logger:         quietLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, mcpPath, strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code == http.StatusOK {
		t.Errorf("status = 200, want the transcoder to reject it: %s", recorder.Body)
	}
	if strings.Contains(recorder.Body.String(), "tools") {
		t.Errorf("body = %s, want no tool listing", recorder.Body)
	}
}

type stubTranscoder struct {
	status      int
	body        string
	request     *http.Request
	requestBody string
}

func (s *stubTranscoder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body := new(bytes.Buffer)
	if r.Body != nil {
		if _, err := body.ReadFrom(r.Body); err != nil {
			panic(err)
		}
	}
	s.request = r
	s.requestBody = body.String()

	if _, ok := w.(http.Flusher); !ok {
		panic("response writer does not implement http.Flusher, which the transcoder requires")
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(s.status)
	_, _ = w.Write([]byte(s.body))
}

func mcpTestHandler(t *testing.T) (http.Handler, *stubTranscoder) {
	t.Helper()

	upstream := &stubTranscoder{status: http.StatusOK, body: `{"id":"thing_1"}`}
	handler, tools := newMCPHandler(testFiles(t), upstream, "test", quietLogger())
	if tools != 2 {
		t.Fatalf("tools = %d, want 2", tools)
	}
	return handler, upstream
}

func callMCP(t *testing.T, handler http.Handler, body string) json.RawMessage {
	t.Helper()

	request := httptest.NewRequest(http.MethodPost, mcpPath, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body)
	}

	var response struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decoding %s: %v", recorder.Body, err)
	}
	if response.Error != nil {
		t.Fatalf("jsonrpc error: %s", response.Error)
	}
	return response.Result
}

func quietLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
