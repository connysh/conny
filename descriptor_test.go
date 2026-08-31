package conny

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const chainDepth = maxSchemaDepth + 3

func testDescriptorSet(t *testing.T) *descriptorpb.FileDescriptorSet {
	t.Helper()

	request := &descriptorpb.DescriptorProto{
		Name: proto.String("Request"),
		Field: []*descriptorpb.FieldDescriptorProto{
			scalarField("name", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING),
			scalarField("count", 2, descriptorpb.FieldDescriptorProto_TYPE_INT32),
			scalarField("total", 3, descriptorpb.FieldDescriptorProto_TYPE_INT64),
			scalarField("size", 4, descriptorpb.FieldDescriptorProto_TYPE_UINT64),
			scalarField("enabled", 5, descriptorpb.FieldDescriptorProto_TYPE_BOOL),
			scalarField("payload", 6, descriptorpb.FieldDescriptorProto_TYPE_BYTES),
			scalarField("ratio", 7, descriptorpb.FieldDescriptorProto_TYPE_DOUBLE),
			scalarField("weight", 8, descriptorpb.FieldDescriptorProto_TYPE_FLOAT),
			scalarField("offset", 9, descriptorpb.FieldDescriptorProto_TYPE_SINT32),
			scalarField("token", 10, descriptorpb.FieldDescriptorProto_TYPE_FIXED64),
			enumField("status", 11, ".test.v1.Status"),
			repeated(scalarField("tags", 12, descriptorpb.FieldDescriptorProto_TYPE_STRING)),
			messageField("child", 13, ".test.v1.Child"),
			repeated(messageField("children", 14, ".test.v1.Child")),
			mapField("labels", 15, ".test.v1.Request.LabelsEntry"),
			mapField("by_id", 16, ".test.v1.Request.ByIdEntry"),
			mapField("by_flag", 17, ".test.v1.Request.ByFlagEntry"),
			mapField("by_index", 18, ".test.v1.Request.ByIndexEntry"),
			messageField("created_at", 19, ".google.protobuf.Timestamp"),
			messageField("timeout", 20, ".google.protobuf.Duration"),
			messageField("metadata", 21, ".google.protobuf.Struct"),
			messageField("anything", 22, ".google.protobuf.Value"),
			messageField("items", 23, ".google.protobuf.ListValue"),
			messageField("update_mask", 24, ".google.protobuf.FieldMask"),
			messageField("detail", 25, ".google.protobuf.Any"),
			messageField("nothing", 26, ".google.protobuf.Empty"),
			messageField("note", 27, ".google.protobuf.StringValue"),
			messageField("big", 28, ".google.protobuf.Int64Value"),
			messageField("flag", 29, ".google.protobuf.BoolValue"),
			messageField("tree", 30, ".test.v1.Node"),
			messageField("chain", 31, ".test.v1.Chain0"),
			oneofMember(scalarField("by_email", 32, descriptorpb.FieldDescriptorProto_TYPE_STRING), 0),
			oneofMember(scalarField("by_phone", 33, descriptorpb.FieldDescriptorProto_TYPE_STRING), 0),
			proto3Optional(scalarField("nickname", 34, descriptorpb.FieldDescriptorProto_TYPE_STRING), 1),
		},
		OneofDecl: []*descriptorpb.OneofDescriptorProto{
			{Name: proto.String("target")},
			{Name: proto.String("_nickname")},
		},
		NestedType: []*descriptorpb.DescriptorProto{
			mapEntry("LabelsEntry",
				descriptorpb.FieldDescriptorProto_TYPE_STRING,
				descriptorpb.FieldDescriptorProto_TYPE_STRING),
			mapEntry("ByIdEntry",
				descriptorpb.FieldDescriptorProto_TYPE_INT64,
				descriptorpb.FieldDescriptorProto_TYPE_STRING),
			mapEntry("ByFlagEntry",
				descriptorpb.FieldDescriptorProto_TYPE_BOOL,
				descriptorpb.FieldDescriptorProto_TYPE_STRING),
			mapEntry("ByIndexEntry",
				descriptorpb.FieldDescriptorProto_TYPE_UINT32,
				descriptorpb.FieldDescriptorProto_TYPE_STRING),
		},
	}

	messages := []*descriptorpb.DescriptorProto{
		request,
		{
			Name: proto.String("Child"),
			Field: []*descriptorpb.FieldDescriptorProto{
				scalarField("id", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING),
			},
		},
		{
			Name: proto.String("Node"),
			Field: []*descriptorpb.FieldDescriptorProto{
				scalarField("id", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING),
				repeated(messageField("children", 2, ".test.v1.Node")),
			},
		},
		{
			Name: proto.String("Response"),
			Field: []*descriptorpb.FieldDescriptorProto{
				scalarField("id", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING),
			},
		},
	}

	for i := range chainDepth {
		chain := &descriptorpb.DescriptorProto{Name: proto.String(fmt.Sprintf("Chain%d", i))}
		if i < chainDepth-1 {
			chain.Field = []*descriptorpb.FieldDescriptorProto{
				messageField("next", 1, fmt.Sprintf(".test.v1.Chain%d", i+1)),
			}
		}
		messages = append(messages, chain)
	}

	getOptions := &descriptorpb.MethodOptions{}
	proto.SetExtension(getOptions, annotations.E_Http, &annotations.HttpRule{
		Pattern: &annotations.HttpRule_Get{Get: "/v1/things"},
	})

	file := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("test/v1/test.proto"),
		Package: proto.String("test.v1"),
		Syntax:  proto.String("proto3"),
		Dependency: []string{
			"google/protobuf/any.proto",
			"google/protobuf/duration.proto",
			"google/protobuf/empty.proto",
			"google/protobuf/field_mask.proto",
			"google/protobuf/struct.proto",
			"google/protobuf/timestamp.proto",
			"google/protobuf/wrappers.proto",
		},
		MessageType: messages,
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name: proto.String("Status"),
			Value: []*descriptorpb.EnumValueDescriptorProto{
				{Name: proto.String("STATUS_UNSPECIFIED"), Number: proto.Int32(0)},
				{Name: proto.String("STATUS_ACTIVE"), Number: proto.Int32(1)},
			},
		}},
		SourceCodeInfo: &descriptorpb.SourceCodeInfo{
			Location: []*descriptorpb.SourceCodeInfo_Location{
				{
					Path:            []int32{6, 0, 2, 0},
					Span:            []int32{0, 0, 1},
					LeadingComments: proto.String(" Fetches one thing.\n Second line.\n"),
				},
				{
					Path:            []int32{4, 0, 2, 0},
					Span:            []int32{0, 0, 1},
					LeadingComments: proto.String(" The thing's name.\n"),
				},
			},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("TestService"),
			Method: []*descriptorpb.MethodDescriptorProto{
				{
					Name:       proto.String("GetThing"),
					InputType:  proto.String(".test.v1.Request"),
					OutputType: proto.String(".test.v1.Response"),
					Options:    getOptions,
				},
				{
					Name:       proto.String("DoThing"),
					InputType:  proto.String(".test.v1.Request"),
					OutputType: proto.String(".test.v1.Response"),
				},
				{
					Name:            proto.String("WatchThings"),
					InputType:       proto.String(".test.v1.Request"),
					OutputType:      proto.String(".test.v1.Response"),
					ServerStreaming: proto.Bool(true),
				},
			},
		}},
	}

	fds := &descriptorpb.FileDescriptorSet{File: wellKnownFiles(t)}
	fds.File = append(fds.File, file)
	return fds
}

func wellKnownFiles(t *testing.T) []*descriptorpb.FileDescriptorProto {
	t.Helper()

	messages := []proto.Message{
		&anypb.Any{},
		&durationpb.Duration{},
		&emptypb.Empty{},
		&fieldmaskpb.FieldMask{},
		&structpb.Struct{},
		&timestamppb.Timestamp{},
		&wrapperspb.StringValue{},
	}

	var files []*descriptorpb.FileDescriptorProto
	seen := map[string]bool{}
	for _, message := range messages {
		fdp := protodesc.ToFileDescriptorProto(message.ProtoReflect().Descriptor().ParentFile())
		if seen[fdp.GetName()] {
			continue
		}
		seen[fdp.GetName()] = true
		files = append(files, fdp)
	}
	return files
}

func testFiles(t *testing.T) *protoregistry.Files {
	t.Helper()

	files, err := protodesc.NewFiles(testDescriptorSet(t))
	if err != nil {
		t.Fatalf("building file registry: %v", err)
	}
	return files
}

func testDescriptorPath(t *testing.T) string {
	t.Helper()

	data, err := proto.Marshal(testDescriptorSet(t))
	if err != nil {
		t.Fatalf("marshalling descriptor set: %v", err)
	}
	path := filepath.Join(t.TempDir(), "test.pb")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func testMessage(t *testing.T, name string) protoreflect.MessageDescriptor {
	t.Helper()

	desc, err := testFiles(t).FindDescriptorByName(protoreflect.FullName(name))
	if err != nil {
		t.Fatalf("finding %s: %v", name, err)
	}
	md, ok := desc.(protoreflect.MessageDescriptor)
	if !ok {
		t.Fatalf("%s is a %T, want a message", name, desc)
	}
	return md
}

func testMethod(t *testing.T, name string) protoreflect.MethodDescriptor {
	t.Helper()

	desc, err := testFiles(t).FindDescriptorByName(protoreflect.FullName(name))
	if err != nil {
		t.Fatalf("finding %s: %v", name, err)
	}
	md, ok := desc.(protoreflect.MethodDescriptor)
	if !ok {
		t.Fatalf("%s is a %T, want a method", name, desc)
	}
	return md
}

func scalarField(name string, number int32, typ descriptorpb.FieldDescriptorProto_Type) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name:     proto.String(name),
		JsonName: proto.String(jsonName(name)),
		Number:   proto.Int32(number),
		Type:     typ.Enum(),
		Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
	}
}

func messageField(name string, number int32, typeName string) *descriptorpb.FieldDescriptorProto {
	fd := scalarField(name, number, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE)
	fd.TypeName = proto.String(typeName)
	return fd
}

func enumField(name string, number int32, typeName string) *descriptorpb.FieldDescriptorProto {
	fd := scalarField(name, number, descriptorpb.FieldDescriptorProto_TYPE_ENUM)
	fd.TypeName = proto.String(typeName)
	return fd
}

func repeated(fd *descriptorpb.FieldDescriptorProto) *descriptorpb.FieldDescriptorProto {
	fd.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	return fd
}

func mapField(name string, number int32, entryTypeName string) *descriptorpb.FieldDescriptorProto {
	return repeated(messageField(name, number, entryTypeName))
}

func mapEntry(name string, key, value descriptorpb.FieldDescriptorProto_Type) *descriptorpb.DescriptorProto {
	return &descriptorpb.DescriptorProto{
		Name: proto.String(name),
		Field: []*descriptorpb.FieldDescriptorProto{
			scalarField("key", 1, key),
			scalarField("value", 2, value),
		},
		Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
	}
}

func oneofMember(fd *descriptorpb.FieldDescriptorProto, oneofIndex int32) *descriptorpb.FieldDescriptorProto {
	fd.OneofIndex = proto.Int32(oneofIndex)
	return fd
}

func proto3Optional(fd *descriptorpb.FieldDescriptorProto, oneofIndex int32) *descriptorpb.FieldDescriptorProto {
	fd.OneofIndex = proto.Int32(oneofIndex)
	fd.Proto3Optional = proto.Bool(true)
	return fd
}

// jsonName lowerCamelCases a field name, which protoc would otherwise fill in.
func jsonName(name string) string {
	out := make([]byte, 0, len(name))
	upper := false
	for i := range len(name) {
		c := name[i]
		if c == '_' {
			upper = true
			continue
		}
		if upper && c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		upper = false
		out = append(out, c)
	}
	return string(out)
}
