package main

import (
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strconv"

	"github.com/connysh/conny"
)

var Version = "dev"

func main() {
	const (
		usageVersion    = "print version"
		usagePort       = "listen port"
		usageDescriptor = "path to proto descriptor file"
		usageProtocol   = "upstream protocol (connect, grpc, grpcweb)"
		usageReflection = "enable server reflection"
		usageStatic     = "directory of static files to serve alongside the RPC routes (e.g. a pre-generated openapi.json)"
		usageMCP        = "serve an MCP endpoint at /mcp exposing unary RPCs as tools"
		usagePayment    = "translate the upstream's Machine Payments Protocol flow: HTTP 402 for REST clients, the MPP MCP binding for MCP clients"
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

	var staticDir string
	defaultStatic := os.Getenv("STATIC")
	flag.StringVar(&staticDir, "static", defaultStatic, usageStatic)

	var enableMCP bool
	defaultMCP := envOrDefaultBool("MCP", false)
	flag.BoolVar(&enableMCP, "mcp", defaultMCP, usageMCP)

	var enablePayment bool
	defaultPayment := envOrDefaultBool("PAYMENT", false)
	flag.BoolVar(&enablePayment, "payment", defaultPayment, usagePayment)

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Conny: A tiny ConnectRPC gateway\n\nUsage: conny -d <descriptor.pb> [flags] <url>\n\nFlags:\n")
		fmt.Fprintf(os.Stderr, "  -d, --descriptor string\n        %s\n", usageDescriptor)
		fmt.Fprintf(os.Stderr, "  -p, --port string\n        %s (default %q)\n", usagePort, defaultPort)
		fmt.Fprintf(os.Stderr, "      --protocol string\n        %s (default %q)\n", usageProtocol, defaultProtocol)
		fmt.Fprintf(os.Stderr, "      --reflection\n        %s (default %t)\n", usageReflection, defaultReflection)
		fmt.Fprintf(os.Stderr, "      --static string\n        %s\n", usageStatic)
		fmt.Fprintf(os.Stderr, "      --mcp\n        %s (default %t)\n", usageMCP, defaultMCP)
		fmt.Fprintf(os.Stderr, "      --payment\n        %s (default %t)\n", usagePayment, defaultPayment)
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

	cfg := conny.Config{
		DescriptorPath: descriptor,
		Target:         rawURL,
		Protocol:       protocol,
		Reflection:     enableReflection,
		StaticDir:      staticDir,
		MCP:            enableMCP,
		Payment:        enablePayment,
		Version:        Version,
	}

	addr := fmt.Sprintf(":%s", port)
	slog.Info("starting gateway", "addr", addr, "target", rawURL, "protocol", protocol)

	if err := conny.ListenAndServe(addr, cfg); err != nil {
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
