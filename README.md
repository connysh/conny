# Conny

A tiny [ConnectRPC](https://connectrpc.com) gateway. Translates REST, gRPC, and gRPC-Web requests into the Connect, gRPC, or gRPC-Web protocols using a protobuf descriptor. 

## Install

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
go install github.com/connysh/conny@latest
```

## Usage

```sh
conny -d descriptor.pb http://localhost:8080
```

### Flags

| Flag | Env | Default | Description |
|------|-----|---------|-------------|
| `-d, --descriptor` | `DESCRIPTOR` | | Path to proto descriptor file |
| `-p, --port`       | `PORT`       | `8888` | Listen port |
| `--protocol`       | `PROTOCOL`   | `connect` | Upstream protocol (`connect`, `grpc`, `grpcweb`) |
| `--reflection`     | `REFLECTION` | `false` | Enable server reflection |
| `-v, --version`    | | | Print version |

The backend URL can also be set via the `URL` environment variable.

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

## License

[Apache 2.0](LICENSE)
