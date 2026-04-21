package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"

	_ "buf.build/gen/go/grpc/grpc/protocolbuffers/go/grpc/reflection/v1"
	"golang.org/x/net/http2"
	"connectrpc.com/grpcreflect"
	"connectrpc.com/vanguard"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

var Version = "dev"

type stringSliceFlag []string

func (f *stringSliceFlag) String() string { return strings.Join(*f, ", ") }
func (f *stringSliceFlag) Set(s string) error {
	*f = append(*f, s)
	return nil
}

func parseHeaders(raw []string) (http.Header, error) {
	h := make(http.Header)
	for _, s := range raw {
		k, v, ok := strings.Cut(s, ":")
		if !ok {
			return nil, fmt.Errorf("invalid header %q: expected \"Name: Value\"", s)
		}
		h.Set(strings.TrimSpace(k), strings.TrimSpace(v))
	}
	return h, nil
}

func main() {
	const (
		usageVersion    = "print version"
		usagePort       = "listen port"
		usageDescriptor = "path to proto descriptor file"
		usageProtocol   = "upstream protocol (connect, grpc, grpcweb)"
		usageReflection = "enable server reflection"
		usageHeader     = "add upstream header (\"Name: Value\"), repeatable; also via HEADER_<NAME>=<value> env vars"
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

	var enableReflection bool
	defaultReflection := envOrDefaultBool("REFLECTION", false)
	flag.BoolVar(&enableReflection, "reflection", defaultReflection, usageReflection)

	var headerFlags stringSliceFlag
	flag.Var(&headerFlags, "H", usageHeader)
	flag.Var(&headerFlags, "header", usageHeader)

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Conny: A tiny ConnectRPC gateway\n\nUsage: conny -d <descriptor.pb> [flags] <url>\n\nFlags:\n")
		fmt.Fprintf(os.Stderr, "  -d, --descriptor string\n        %s\n", usageDescriptor)
		fmt.Fprintf(os.Stderr, "  -H, --header string\n        %s\n", usageHeader)
		fmt.Fprintf(os.Stderr, "  -p, --port string\n        %s (default %q)\n", usagePort, defaultPort)
		fmt.Fprintf(os.Stderr, "      --protocol string\n        %s (default %q)\n", usageProtocol, defaultProtocol)
		fmt.Fprintf(os.Stderr, "      --reflection\n        %s (default %t)\n", usageReflection, defaultReflection)
		fmt.Fprintf(os.Stderr, "  -v, --version\n        %s\n", usageVersion)
	}
	flag.Parse()

	var envHeaders stringSliceFlag
	for _, env := range os.Environ() {
		k, v, ok := strings.Cut(env, "=")
		if ok && strings.HasPrefix(k, "HEADER_") {
			name := strings.ReplaceAll(strings.TrimPrefix(k, "HEADER_"), "_", "-")
			envHeaders = append(envHeaders, name+": "+v)
		}
	}
	headerFlags = append(envHeaders, headerFlags...)

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
	var enableH2C bool
	if strings.HasPrefix(rawURL, "h2c://") {
		enableH2C = true
		rawURL = "http://" + strings.TrimPrefix(rawURL, "h2c://")
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

	upstreamHeaders, err := parseHeaders(headerFlags)
	if err != nil {
		log.Fatalf("invalid header: %v", err)
	}

	fds, err := loadDescriptorSet(descriptor)
	if err != nil {
		log.Fatalf("failed to load descriptor set: %v", err)
	}
	slog.Info("loaded descriptor set", "files", len(fds.GetFile()))

	services, err := buildServices(fds, targetURL, vanguardProto, enableReflection, enableH2C, upstreamHeaders)
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

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte("ok\n"))
		}
	})
	mux.Handle("/", transcoder)

	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)

	server := &http.Server{
		Addr:      addr,
		Handler:   mux,
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

func envOrDefaultBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		parsed, err := strconv.ParseBool(v)
		if err == nil {
			return parsed
		}
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

func buildServices(fds *descriptorpb.FileDescriptorSet, targetURL *url.URL, protocol vanguard.Protocol, enableReflection, enableH2C bool, extraHeaders http.Header) ([]*vanguard.Service, error) {
	files, err := protodesc.NewFiles(fds)
	if err != nil {
		return nil, fmt.Errorf("creating file registry: %w", err)
	}

	types := dynamicpb.NewTypes(files)
	proxy := newReverseProxy(targetURL, enableH2C, extraHeaders)

	var services []*vanguard.Service
	var serviceNames []string

	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		sds := fd.Services()
		for i := range sds.Len() {
			sd := sds.Get(i)
			serviceNames = append(serviceNames, string(sd.FullName()))
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

	if enableReflection {
		reflector := grpcreflect.NewReflector(
			&namer{services: serviceNames},
			grpcreflect.WithDescriptorResolver(files),
			grpcreflect.WithExtensionResolver(&extensionResolver{types}),
		)

		v1Path, v1Handler := grpcreflect.NewHandlerV1(reflector)
		services = append(services, vanguard.NewService(v1Path, v1Handler))
		slog.Info("registered reflection service", "version", "v1", "path", v1Path)

	}

	return services, nil
}

func newReverseProxy(target *url.URL, enableH2C bool, extraHeaders http.Header) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = target.Host
		for k, vs := range extraHeaders {
			req.Header[k] = vs
		}
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Del("Content-Length")
		resp.ContentLength = -1
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		slog.Error("upstream error", "method", r.Method, "path", r.URL.Path, "error", err)
		w.WriteHeader(http.StatusBadGateway)
	}
	if enableH2C {
		proxy.Transport = &http2.Transport{
			AllowHTTP: true,
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				return net.Dial(network, addr)
			},
		}
	}
	return proxy
}

type extensionResolver struct {
	*dynamicpb.Types
}

func (e *extensionResolver) RangeExtensionsByMessage(message protoreflect.FullName, f func(protoreflect.ExtensionType) bool) {
}

type namer struct {
	services []string
}

func (n *namer) Names() []string {
	return n.services
}
