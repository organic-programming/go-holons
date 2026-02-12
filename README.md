# go-holons

**Go SDK for Organic Programming** — reference implementations of the
plumbing that every Go holon needs.

This is **not** a holon. It is a library. Holons import it.

## Packages

| Package | Import path | Purpose |
|---------|-------------|---------|
| `transport` | `go-holons/pkg/transport` | URI → `net.Listener` factory |
| `serve` | `go-holons/pkg/serve` | Standard `serve` command |
| `grpcclient` | `go-holons/pkg/grpcclient` | Transport-agnostic gRPC client |

## Transports

| Scheme | Description |
|--------|-------------|
| `tcp://<host>:<port>` | TCP socket, classic gRPC |
| `unix://<path>` | Unix domain socket, local IPC |
| `stdio://` | stdin/stdout pipe (LSP-style) |
| `mem://` | In-process bufconn (testing, composite holons) |
| `ws://<host>:<port>` | WebSocket (browser, NAT traversal) |
| `wss://<host>:<port>` | WebSocket over TLS |

See [AGENT.md](./AGENT.md) for full documentation.
