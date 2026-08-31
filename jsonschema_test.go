package conny

import (
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestFieldSchemas(t *testing.T) {
	properties := requestProperties(t, "test.v1.Request")

	tests := []struct {
		field string
		want  string
	}{
		{"name", `{"description":"The thing's name.","type":"string"}`},
		{"count", `{"type":"integer"}`},
		{"offset", `{"type":"integer"}`},
		{"enabled", `{"type":"boolean"}`},
		{"ratio", `{"type":"number"}`},
		{"weight", `{"type":"number"}`},
		{"payload", `{"contentEncoding":"base64","type":"string"}`},
		{"total", `{"type":["string","integer"]}`},
		{"size", `{"type":["string","integer"]}`},
		{"token", `{"type":["string","integer"]}`},
		{"status", `{"enum":["STATUS_UNSPECIFIED","STATUS_ACTIVE"],"type":"string"}`},
		{"tags", `{"items":{"type":"string"},"type":"array"}`},
		{"child", `{"additionalProperties":false,"properties":{"id":{"type":"string"}},"type":"object"}`},
		{"children", `{"items":{"additionalProperties":false,"properties":{"id":{"type":"string"}},"type":"object"},"type":"array"}`},
		{"labels", `{"additionalProperties":{"type":"string"},"type":"object"}`},
		{"by_id", `{"additionalProperties":{"type":"string"},"propertyNames":{"pattern":"^-?[0-9]+$"},"type":"object"}`},
		{"by_index", `{"additionalProperties":{"type":"string"},"propertyNames":{"pattern":"^[0-9]+$"},"type":"object"}`},
		{"by_flag", `{"additionalProperties":{"type":"string"},"propertyNames":{"pattern":"^(true|false)$"},"type":"object"}`},
		{"created_at", `{"description":"RFC 3339 timestamp, e.g. \"2026-01-02T15:04:05Z\"","format":"date-time","type":"string"}`},
		{"timeout", `{"description":"duration in seconds, e.g. \"1.5s\"","pattern":"^-?[0-9]+(\\.[0-9]{1,9})?s$","type":"string"}`},
		{"update_mask", `{"description":"comma-separated list of field paths","type":"string"}`},
		{"metadata", `{"type":"object"}`},
		{"items", `{"type":"array"}`},
		{"anything", `{"description":"any JSON value"}`},
		{"detail", `{"description":"the message's own fields, plus \"@type\" naming its proto type","properties":{"@type":{"type":"string"}},"required":["@type"],"type":"object"}`},
		{"nothing", `{"additionalProperties":false,"properties":{},"type":"object"}`},
		{"note", `{"type":"string"}`},
		{"big", `{"type":["string","integer"]}`},
		{"flag", `{"type":"boolean"}`},
		{"by_email", `{"description":"part of oneof \"target\": set at most one of by_email, by_phone","type":"string"}`},
		{"by_phone", `{"description":"part of oneof \"target\": set at most one of by_email, by_phone","type":"string"}`},
		{"nickname", `{"type":"string"}`},
	}
	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			property, ok := properties[tt.field]
			if !ok {
				t.Fatalf("no schema for field %q", tt.field)
			}
			if got := marshal(t, property); got != tt.want {
				t.Errorf("schema = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestRequestSchemaIsClosedObject(t *testing.T) {
	schema := requestSchema(testMessage(t, "test.v1.Request"))

	if schema["type"] != "object" {
		t.Errorf("type = %v, want object", schema["type"])
	}
	if schema["additionalProperties"] != false {
		t.Errorf("additionalProperties = %v, want false", schema["additionalProperties"])
	}
	if _, ok := schema["properties"].(map[string]any); !ok {
		t.Errorf("properties = %T, want a map", schema["properties"])
	}
}

func TestRequestSchemaNonObjectRequest(t *testing.T) {
	md := (&timestamppb.Timestamp{}).ProtoReflect().Descriptor()

	want := `{"description":"JSON encoding of google.protobuf.Timestamp","type":"object"}`
	if got := marshal(t, requestSchema(md)); got != want {
		t.Errorf("schema = %s, want %s", got, want)
	}
}

func TestSchemaStopsAtRecursion(t *testing.T) {
	properties := requestProperties(t, "test.v1.Request")

	want := `{"additionalProperties":false,"properties":{"children":{"items":{"description":"nested test.v1.Node, omitted to keep this schema finite","type":"object"},"type":"array"},"id":{"type":"string"}},"type":"object"}`
	if got := marshal(t, properties["tree"]); got != want {
		t.Errorf("schema = %s, want %s", got, want)
	}
}

func TestSchemaStopsAtDepthLimit(t *testing.T) {
	properties := requestProperties(t, "test.v1.Request")

	schema, ok := properties["chain"].(map[string]any)
	if !ok {
		t.Fatalf("chain schema = %T, want a map", properties["chain"])
	}
	depth := 1
	for {
		nested, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("no properties at depth %d: %s", depth, marshal(t, schema))
		}
		next, ok := nested["next"].(map[string]any)
		if !ok {
			t.Fatalf("no next field at depth %d: %s", depth, marshal(t, schema))
		}
		if description, ok := next["description"].(string); ok &&
			strings.Contains(description, "omitted to keep this schema finite") {
			break
		}
		schema = next
		depth++
		if depth > chainDepth {
			t.Fatalf("chain expanded past %d messages without stopping", chainDepth)
		}
	}

	// The request message itself occupies the first level, so Chain0 through
	// Chain8 expand and Chain9 is where the walk stops.
	if depth != maxSchemaDepth-1 {
		t.Errorf("expanded %d chained messages, want %d", depth, maxSchemaDepth-1)
	}
}

func TestComment(t *testing.T) {
	md := testMethod(t, "test.v1.TestService.GetThing")
	if got, want := comment(md), "Fetches one thing.\nSecond line."; got != want {
		t.Errorf("comment = %q, want %q", got, want)
	}

	if got := comment(testMessage(t, "test.v1.Child")); got != "" {
		t.Errorf("comment = %q, want empty", got)
	}
}

func requestProperties(t *testing.T, message string) map[string]any {
	t.Helper()

	schema := requestSchema(testMessage(t, message))
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %T, want a map", schema["properties"])
	}
	return properties
}

func marshal(t *testing.T, v any) string {
	t.Helper()

	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
