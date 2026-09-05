# Conny

A tiny [ConnectRPC](https://connectrpc.com) gateway. Translates REST, gRPC, and gRPC-Web requests into the Connect, gRPC, or gRPC-Web protocols using a protobuf descriptor. 

## Install

**Homebrew:**

```sh
brew install connysh/tap/conny
```

**Binary** (from [GitHub Releases](https://github.com/connysh/conny/releases)):

```sh
# macOS / Linux
curl -fsSL https://github.com/connysh/conny/releases/latest/download/conny_$(uname -s)_$(uname -m).tar.gz | tar xz
```

**Docker:**

```sh
docker pull ghcr.io/connysh/conny:latest
```

**Go:**

```sh
go install github.com/connysh/conny/cmd/conny@latest
```

## Usage

```sh
conny -d descriptor.pb http://localhost:8080
```

Use `h2c://` for upstream servers that require HTTP/2 over plaintext (e.g. gRPC with streaming):

```sh
conny -d descriptor.pb h2c://localhost:8080
```

### Flags

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `-d, --descriptor` | `DESCRIPTOR` | | Path to proto descriptor file |
| `-p, --port`       | `PORT`       | `8888` | Listen port |
| `--protocol`       | `PROTOCOL`   | `connect` | Upstream protocol (`connect`, `grpc`, `grpcweb`) |
| `--reflection`     | `REFLECTION` | `false` | Enable server reflection |
| `--static`         | `STATIC`     | | Directory of static files to serve alongside the RPC routes |
| `--mcp`            | `MCP`        | `false` | Serve an MCP endpoint at `/mcp` exposing unary RPCs as tools |
| `--mpp`            | `MPP`        | `false` | Translate the upstream's [Machine Payments Protocol](https://mpp.dev) flow: HTTP 402 for REST clients, the MPP MCP binding for MCP clients |
| `-v, --version`    | | | Print version |

The backend URL can also be set via the `URL` environment variable.
`--payment` and `PAYMENT` still work as deprecated aliases of `--mpp` and `MPP`.

### Health check

`GET /health` returns `200 OK` with body `ok`.

### Static files

`--static <dir>` serves files alongside the RPC routes — a pre-generated
`openapi.json`, a docs page, a favicon. Requests that don't name a file fall
through to the transcoder, so RPCs are unaffected. Dot-prefixed names and
directories without an `index.html` are never served.

```sh
conny -d descriptor.pb --static ./public http://localhost:8080
curl localhost:8888/openapi.json
```

### MCP

`--mcp` serves a [Model Context Protocol](https://modelcontextprotocol.io)
endpoint at `/mcp`, exposing every unary method as a tool an agent can call.
Tools are named for the method's full proto name with dots replaced by
underscores, take the request message as JSON, and are documented from its
`.proto` comments. Methods bound to HTTP `GET` are marked read-only, streaming
methods are skipped, and a caller's `Authorization` header is passed upstream.
Calls take the same path as any other client's, so `--protocol` applies.

With `--mpp`, tool calls also follow the MPP MCP binding, so an agent can pay
a per-call upstream without leaving MCP. See [MPP](#mpp).

```sh
conny -d descriptor.pb --mcp http://localhost:8080
npx @modelcontextprotocol/inspector http://localhost:8888/mcp
```

### MPP

`--mpp` translates the upstream's
[Machine Payments Protocol](https://mpp.dev) flow for clients that expect its
native shape. The upstream stays in charge: it prices each call, issues the
`Payment` challenge, and verifies the credential. Conny holds no keys, keeps no
state, and never decides whether a call is paid for.

**REST clients.** gRPC and Connect have no `402`, so an MPP upstream answers an
unpaid call with `401 Unauthenticated` plus a `WWW-Authenticate: Payment`
challenge. Conny rewrites the status to `402 Payment Required` for REST
callers, which is what MPP clients look for over HTTP. Headers and body pass
through untouched, and RPC callers see the `401` unchanged, since their clients
read the code from the body.

**MCP clients.** With `--mcp`, tool calls follow the
[MPP MCP binding](https://mpp.dev/protocol/transports/mcp):

| Upstream (HTTP) | MCP client sees |
|-----------------|-----------------|
| `401` + `WWW-Authenticate: Payment` | JSON-RPC error `-32042` with the challenges as JSON in `error.data.challenges` |
| `Authorization: Payment <credential>` | sent by the client as `_meta["org.paymentauth/credential"]` |
| challenge after a credential was sent | error `-32043` with a fresh challenge and `error.data.failure.reason` |
| `Payment-Receipt` on success | `_meta["org.paymentauth/receipt"]` on the tool result |

A credential that is not an object with a `challenge.id` and a `payload` is
rejected with `-32602` before anything reaches the upstream. A `403` without a
challenge is a plain tool error, not a payment error: the payment was fine and
policy denied access.

```sh
conny -d descriptor.pb --mcp --mpp http://localhost:8080
```

**Constraints.**

- The upstream must speak MPP over HTTP. Conny does not price calls or verify
  payments; gateway-side pricing is not supported.
- The MCP client must implement the MPP binding. Hosts that don't will surface
  `-32042` as an ordinary error.
- Each challenge must arrive in its own `WWW-Authenticate` header with the
  required `id`, `realm`, `method`, `intent`, and base64url `request`
  parameters. Anything else is left as a plain error.
- The client echoes `challenge.request` as JSON; conny re-encodes it to the
  base64url string the upstream issued. That round-trips exactly for the flat,
  string-valued requests MPP payment methods use.
- `failure.reason` is inferred: from an RFC 9457 problem body if the upstream
  sends one, else `payment-expired` when the echoed challenge has expired, else
  `malformed-credential` on a Connect `invalid_argument`, else
  `verification-failed`. Reasons only the upstream knows, such as
  `payment-insufficient`, need a problem body.
- The binding is IETF draft `draft-payment-transport-mcp-00`; its error codes
  and `_meta` keys may still change.

### Generate a descriptor

```sh
buf build -o descriptor.pb
```

### Docker

```sh
docker run -v ./descriptor.pb:/descriptor.pb ghcr.io/connysh/conny \
  -d /descriptor.pb http://backend:8080
```

### Kubernetes

```yaml
containers:
  - name: conny
    image: ghcr.io/connysh/conny:latest
    args: ["-d", "/etc/conny/descriptor.pb", "http://backend:8080"]
    volumeMounts:
      - name: descriptor
        mountPath: /etc/conny
volumes:
  - name: descriptor
    configMap:
      name: conny-descriptor
```

## Use as a library

conny can also be embedded in a Go program. Build a `conny.Config` and either
serve it directly or mount its `http.Handler` in your own server:

```go
package main

import (
	"log"
	"net/http"

	"github.com/connysh/conny"
)

func main() {
	cfg := conny.Config{
		DescriptorPath: "descriptor.pb",     // or Descriptor: fds
		Target:         "h2c://localhost:8080",
		Protocol:       "connect",           // connect | grpc | grpcweb
		Reflection:     true,
		StaticDir:      "./public",          // optional
		MCP:            true,
		MPP:            true,
	}

	// Serve directly (HTTP/1 + h2c, blocks):
	log.Fatal(conny.ListenAndServe(":8888", cfg))

	// ...or mount the handler in your own mux / middleware stack:
	h, err := conny.NewHandler(cfg)
	if err != nil {
		log.Fatal(err)
	}
	http.Handle("/api/", http.StripPrefix("/api", h))
}
```

Every CLI flag has a `Config` field. Provide the descriptor either as a path
(`DescriptorPath`) or an in-memory `*descriptorpb.FileDescriptorSet`
(`Descriptor`). Set `Logger` to route structured logs; it defaults to
`slog.Default()`.

## License

[Apache 2.0](LICENSE)
