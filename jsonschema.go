package conny

import (
	"fmt"
	"strings"

	"buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// maxSchemaDepth caps how far nested messages are expanded. Messages may
// reference themselves — directly, or through google.protobuf.Struct — so an
// unbounded walk would not terminate.
const maxSchemaDepth = 10

// requestSchema describes md as a JSON Schema object for use as a tool's input
// schema. MCP requires an object at the top level, so a request message that
// protobuf JSON encodes as something else — a bare string, for a Timestamp — gets
// an open object instead of a schema clients would reject.
func requestSchema(md protoreflect.MessageDescriptor) map[string]any {
	schema := messageSchema(md, map[protoreflect.FullName]bool{}, 0)
	if schema["type"] != "object" {
		return map[string]any{
			"type":        "object",
			"description": fmt.Sprintf("JSON encoding of %s", md.FullName()),
		}
	}
	return schema
}

func messageSchema(md protoreflect.MessageDescriptor, expanding map[protoreflect.FullName]bool, depth int) map[string]any {
	if schema, ok := wellKnownSchema(md); ok {
		return schema
	}
	if expanding[md.FullName()] || depth >= maxSchemaDepth {
		return map[string]any{
			"type":        "object",
			"description": fmt.Sprintf("nested %s, omitted to keep this schema finite", md.FullName()),
		}
	}
	expanding[md.FullName()] = true
	defer delete(expanding, md.FullName())

	properties := map[string]any{}
	var required []string
	fields := md.Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if fd.IsExtension() {
			continue
		}
		properties[fd.TextName()] = fieldSchema(fd, expanding, depth)
		if fieldRules(fd).GetRequired() {
			required = append(required, fd.TextName())
		}
	}

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
		// The transcoder discards unknown fields, so an invented one would fail
		// silently. Closing the object turns that into a visible error.
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	if doc := comment(md); doc != "" {
		schema["description"] = doc
	}
	return schema
}

func fieldSchema(fd protoreflect.FieldDescriptor, expanding map[protoreflect.FullName]bool, depth int) map[string]any {
	var schema map[string]any
	switch {
	case fd.IsMap():
		schema = mapSchema(fd, expanding, depth)
	case fd.IsList():
		schema = map[string]any{"type": "array", "items": valueSchema(fd, expanding, depth)}
	default:
		schema = valueSchema(fd, expanding, depth)
	}

	var notes []string
	if doc := comment(fd); doc != "" {
		notes = append(notes, doc)
	}
	// A proto3 optional field is carried by a one-field synthetic oneof, which
	// says nothing to a caller; only a real oneof is worth describing.
	if od := fd.ContainingOneof(); od != nil && !od.IsSynthetic() {
		notes = append(notes, oneofNote(od))
	}
	if hint, ok := schema["description"].(string); ok && hint != "" {
		notes = append(notes, hint)
	}
	if len(notes) > 0 {
		schema["description"] = strings.Join(notes, "\n")
	}
	return schema
}

// valueSchema describes one value of fd: the element type for a repeated field,
// the field itself otherwise.
func valueSchema(fd protoreflect.FieldDescriptor, expanding map[protoreflect.FullName]bool, depth int) map[string]any {
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return map[string]any{"type": "boolean"}
	case protoreflect.StringKind:
		schema := map[string]any{"type": "string"}
		if in := stringInValues(fd); len(in) > 0 {
			schema["enum"] = in
		}
		return schema
	case protoreflect.BytesKind:
		return map[string]any{"type": "string", "contentEncoding": "base64"}
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return map[string]any{"type": "number"}
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return map[string]any{"type": "integer"}
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		// Written as JSON strings, read from either form. Both are allowed so a
		// value copied out of a response can go straight back into a request.
		return map[string]any{"type": []string{"string", "integer"}}
	case protoreflect.EnumKind:
		return enumSchema(fd.Enum())
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return messageSchema(fd.Message(), expanding, depth+1)
	default:
		return map[string]any{}
	}
}

func mapSchema(fd protoreflect.FieldDescriptor, expanding map[protoreflect.FullName]bool, depth int) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": valueSchema(fd.MapValue(), expanding, depth),
	}
	switch fd.MapKey().Kind() {
	case protoreflect.StringKind:
	case protoreflect.BoolKind:
		schema["propertyNames"] = map[string]any{"pattern": `^(true|false)$`}
	case protoreflect.Uint32Kind, protoreflect.Uint64Kind,
		protoreflect.Fixed32Kind, protoreflect.Fixed64Kind:
		schema["propertyNames"] = map[string]any{"pattern": `^[0-9]+$`}
	default:
		schema["propertyNames"] = map[string]any{"pattern": `^-?[0-9]+$`}
	}
	return schema
}

func enumSchema(ed protoreflect.EnumDescriptor) map[string]any {
	values := ed.Values()
	names := make([]string, 0, values.Len())
	for i := range values.Len() {
		names = append(names, string(values.Get(i).Name()))
	}
	return map[string]any{"type": "string", "enum": names}
}

// wellKnownSchema returns the schema for a google.protobuf well-known type, which
// protobuf JSON encodes as something other than a plain object. The parent check
// mirrors protojson's own, so a local mypkg.Timestamp is not mistaken for one.
func wellKnownSchema(md protoreflect.MessageDescriptor) (map[string]any, bool) {
	if md.FullName().Parent() != "google.protobuf" {
		return nil, false
	}
	switch md.FullName().Name() {
	case "Timestamp":
		return map[string]any{
			"type":        "string",
			"format":      "date-time",
			"description": `RFC 3339 timestamp, e.g. "2026-01-02T15:04:05Z"`,
		}, true
	case "Duration":
		return map[string]any{
			"type":        "string",
			"pattern":     `^-?[0-9]+(\.[0-9]{1,9})?s$`,
			"description": `duration in seconds, e.g. "1.5s"`,
		}, true
	case "FieldMask":
		return map[string]any{
			"type":        "string",
			"description": "comma-separated list of field paths",
		}, true
	case "Struct":
		return map[string]any{"type": "object"}, true
	case "ListValue":
		return map[string]any{"type": "array"}, true
	case "Value":
		return map[string]any{"description": "any JSON value"}, true
	case "Any":
		return map[string]any{
			"type":        "object",
			"properties":  map[string]any{"@type": map[string]any{"type": "string"}},
			"required":    []string{"@type"},
			"description": `the message's own fields, plus "@type" naming its proto type`,
		}, true
	case "Empty":
		return map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		}, true
	case "BoolValue":
		return map[string]any{"type": "boolean"}, true
	case "StringValue":
		return map[string]any{"type": "string"}, true
	case "BytesValue":
		return map[string]any{"type": "string", "contentEncoding": "base64"}, true
	case "FloatValue", "DoubleValue":
		return map[string]any{"type": "number"}, true
	case "Int32Value", "UInt32Value":
		return map[string]any{"type": "integer"}, true
	case "Int64Value", "UInt64Value":
		return map[string]any{"type": []string{"string", "integer"}}, true
	}
	return nil, false
}

// oneofNote describes a oneof group on each of its members, in place of a JSON
// Schema "oneOf" — which models and their validators handle inconsistently.
func oneofNote(od protoreflect.OneofDescriptor) string {
	fields := od.Fields()
	names := make([]string, 0, fields.Len())
	for i := range fields.Len() {
		names = append(names, fields.Get(i).TextName())
	}
	return fmt.Sprintf("part of oneof %q: set at most one of %s", od.Name(), strings.Join(names, ", "))
}

// fieldRules returns fd's buf.validate constraints, or nil if it has none —
// either because the descriptor set never imported buf/validate/validate.proto,
// or because this particular field carries no (buf.validate.field) option. Its
// getters are nil-receiver-safe, so callers use it directly (fieldRules(fd).GetRequired()).
func fieldRules(fd protoreflect.FieldDescriptor) *validate.FieldRules {
	options, ok := fd.Options().(*descriptorpb.FieldOptions)
	if !ok || options == nil {
		return nil
	}
	rules, _ := proto.GetExtension(options, validate.E_Field).(*validate.FieldRules)
	return rules
}

// stringInValues returns the closed set of strings fd is constrained to by a
// buf.validate `string.in` rule — on the field itself for a scalar, or on its
// items for a repeated field — or nil if there is no such constraint.
func stringInValues(fd protoreflect.FieldDescriptor) []string {
	rules := fieldRules(fd)
	if fd.IsList() {
		return rules.GetRepeated().GetItems().GetString_().GetIn()
	}
	return rules.GetString_().GetIn()
}

// comment returns the leading .proto comment for d, empty when the descriptor set
// carries no source info. Every line arrives with the space that followed its
// "//", so each is trimmed rather than just the block.
func comment(d protoreflect.Descriptor) string {
	file := d.ParentFile()
	if file == nil {
		return ""
	}
	raw := file.SourceLocations().ByDescriptor(d).LeadingComments
	if raw == "" {
		return ""
	}
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
