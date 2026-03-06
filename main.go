package main

import (
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"

	"connectrpc.com/vanguard"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

var Version = "dev"

func main() {
	const (
		usageVersion    = "print version"
		usagePort       = "listen port"
		usageDescriptor = "path to proto descriptor file"
		usageProtocol   = "upstream protocol (connect, grpc, grpcweb)"
		usageReflection = "enable server reflection"
	)

	var version bool
	flag.BoolVar(&version, "v", false, usageVersion)
	flag.BoolVar(&version, "version", false, usageVersion)

	var port string
	defaultPort := envOrDefault("PORT", "8888")
	flag.StringVar(&port, "p", defaultPort, usagePort)
	flag.StringVar(&port, "port", defaultPort, usagePort)

	var descriptor string
	defaultDescriptor := os.Getenv("DESCRIPTOR")
	flag.StringVar(&descriptor, "d", defaultDescriptor, usageDescriptor)
	flag.StringVar(&descriptor, "descriptor", defaultDescriptor, usageDescriptor)

	var protocol string
	defaultProtocol := envOrDefault("PROTOCOL", "connect")
	flag.StringVar(&protocol, "protocol", defaultProtocol, usageProtocol)

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Conny: A tiny ConnectRPC gateway\n\nUsage: conny -d <descriptor.pb> [flags] <url>\n\nFlags:\n")
		fmt.Fprintf(os.Stderr, "  -d, --descriptor string\n        %s\n", usageDescriptor)
		fmt.Fprintf(os.Stderr, "  -p, --port string\n        %s (default %q)\n", usagePort, defaultPort)
		fmt.Fprintf(os.Stderr, "      --protocol string\n        %s (default %q)\n", usageProtocol, defaultProtocol)
		fmt.Fprintf(os.Stderr, "  -v, --version\n        %s\n", usageVersion)
	}
	flag.Parse()

	if version {
		fmt.Println(Version)
		os.Exit(0)
	}

	rawURL := flag.Arg(0)
	if rawURL == "" {
		rawURL = os.Getenv("URL")
	}
	if rawURL == "" || descriptor == "" {
		flag.Usage()
		os.Exit(1)
	}
	targetURL, err := url.Parse(rawURL)
	if err != nil {
		log.Fatalf("invalid URL: %v", err)
	}

	var vanguardProto vanguard.Protocol
	switch protocol {
	case "connect":
		vanguardProto = vanguard.ProtocolConnect
	case "grpc":
		vanguardProto = vanguard.ProtocolGRPC
	case "grpcweb", "grpc-web":
		vanguardProto = vanguard.ProtocolGRPCWeb
	default:
		log.Fatalf("invalid protocol: %s (must be connect, grpc, or grpcweb)", protocol)
	}

	fds, err := loadDescriptorSet(descriptor)
	if err != nil {
		log.Fatalf("failed to load descriptor set: %v", err)
	}
	slog.Info("loaded descriptor set", "files", len(fds.GetFile()))

	services, err := buildServices(fds, targetURL, vanguardProto)
	if err != nil {
		log.Fatalf("failed to build services: %v", err)
	}
	slog.Info("registered services", "count", len(services))

	transcoder, err := vanguard.NewTranscoder(services,
		vanguard.WithCodec(func(res vanguard.TypeResolver) vanguard.Codec {
			codec := vanguard.NewJSONCodec(res)
			codec.MarshalOptions.UseProtoNames = true
			codec.UnmarshalOptions.DiscardUnknown = true
			return codec
		}),
	)
	if err != nil {
		log.Fatalf("failed to create transcoder: %v", err)
	}

	addr := fmt.Sprintf(":%s", port)
	slog.Info("starting gateway", "addr", addr, "target", rawURL, "protocol", protocol)

	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)

	server := &http.Server{
		Addr:      addr,
		Handler:   transcoder,
		Protocols: protocols,
	}
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func loadDescriptorSet(path string) (*descriptorpb.FileDescriptorSet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading descriptor file: %w", err)
	}
	fds := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(data, fds); err != nil {
		return nil, fmt.Errorf("unmarshalling descriptor set: %w", err)
	}
	return fds, nil
}

func buildServices(fds *descriptorpb.FileDescriptorSet, targetURL *url.URL, protocol vanguard.Protocol) ([]*vanguard.Service, error) {
	files, err := protodesc.NewFiles(fds)
	if err != nil {
		return nil, fmt.Errorf("creating file registry: %w", err)
	}

	types := dynamicpb.NewTypes(files)
	proxy := newReverseProxy(targetURL)

	var services []*vanguard.Service
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		sds := fd.Services()
		for i := range sds.Len() {
			sd := sds.Get(i)
			svc := vanguard.NewServiceWithSchema(
				sd,
				proxy,
				vanguard.WithTargetProtocols(protocol),
				vanguard.WithTargetCodecs("proto", "json"),
				vanguard.WithTypeResolver(types),
				vanguard.WithRESTUnmarshalOptions(vanguard.RESTUnmarshalOptions{DiscardUnknownQueryParams: true}),
			)
			services = append(services, svc)
			slog.Info("registered service", "name", sd.FullName())
		}
		return true
	})

	if len(services) == 0 {
		return nil, fmt.Errorf("no services found in descriptor set")
	}

	return services, nil
}

func newReverseProxy(target *url.URL) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = target.Host
	}
	return proxy
}
