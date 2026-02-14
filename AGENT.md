---
# Cartouche v1
title: "go-holons — Go SDK for Organic Programming"
author:
  name: "B. ALTER & Claude"
  copyright: "© 2026 Benoit Pereira da Silva"
created: 2026-02-12
revised: 2026-02-14
lang: en-US
origin_lang: en-US
translation_of: null
translator: null
access:
  humans: true
  agents: true
status: draft
---

# go-holons — Go SDK for Organic Programming

`go-holons` is **not a holon**. It is a Go module that provides reference
implementations of the plumbing required by Constitution Article 11
(The Serve Convention). Holons import it; it does not run on its own.

**Import path**: `github.com/Organic-Programming/go-holons/pkg/<package>`

---

## 1. Architecture

```
sdk/go-holons/
├── AGENT.md           ← you are here
├── README.md
├── cert.json
├── go.mod / go.sum
└── pkg/
    ├── transport/     ← URI → net.Listener factory
    ├── serve/         ← standard serve command
    ├── grpcclient/    ← transport-agnostic gRPC client
    └── holonrpc/      ← Holon-RPC client + server (JSON-RPC 2.0 over WebSocket)
```

The four packages map to the four phases of inter-holon communication:

| Phase | Package | Who uses it |
|-------|---------|-------------|
| **Listen** | `transport` | The holon server |
| **Serve** | `serve` | The holon's `serve` subcommand |
| **Dial** | `grpcclient` | The holon client (or OP) |
| **Holon-RPC** | `holonrpc` | Holon-RPC client + server (see [PROTOCOL.md §4](../../PROTOCOL.md#4-holon-rpc-binding)) |

---

## ⚠️ Go Environment — MANDATORY

This project uses a **specific Go installation**. Agents **MUST** use it
for every `go` command — build, test, run, or vet. No exceptions.

```bash
export GOTOOLCHAIN=local
export GOROOT=/Users/bpds/go/go1.25.1
export PATH=/Users/bpds/go/go1.25.1/bin:$PATH
```

Before doing anything else, verify with:

```bash
go version   # must print: go version go1.25.1 darwin/arm64
go env GOROOT  # must print: /Users/bpds/go/go1.25.1
```

**Never** use `/opt/homebrew/bin/go` or any other Go binary.
**Never** let `GOTOOLCHAIN=auto` download or switch to a different version.
If the versions do not match, **stop and ask the user**.

---

## 2. `pkg/transport` — URI-based listener factory

Parses a transport URI and returns a `net.Listener`. This is the
lowest-level building block — use it directly only if `pkg/serve` does
not fit your needs.

### API

```go
import "github.com/Organic-Programming/go-holons/pkg/transport"

// Listen creates a net.Listener from a transport URI.
lis, err := transport.Listen("tcp://:9090")        // TCP socket
lis, err := transport.Listen("unix:///tmp/h.sock")  // Unix domain socket
lis, err := transport.Listen("stdio://")            // stdin/stdout pipe
lis, err := transport.Listen("mem://")              // in-process bufconn
lis, err := transport.Listen("ws://:8080")          // WebSocket

// DefaultURI is "tcp://:9090".
transport.DefaultURI

// Scheme extracts the scheme name for logging.
transport.Scheme("tcp://:9090") // → "tcp"
```

### Supported URI schemes

| Scheme | Description | Mandatory |
|--------|-------------|-----------|
| `tcp://<host>:<port>` | TCP socket, classic gRPC | Yes |
| `stdio://` | stdin/stdout pipe, zero overhead | Yes |
| `unix://<path>` | Unix domain socket, local IPC | Optional |
| `mem://` | In-process bufconn, testing and composite holons | Optional |
| `ws://<host>:<port>[/path]` | WebSocket, browser-accessible, NAT-friendly | Optional |
| `wss://<host>:<port>[/path]` | WebSocket over TLS | Optional |

See [PROTOCOL.md §2.7](../../PROTOCOL.md#27-transport-properties) for per-scheme
valence (mono/multi) and duplex (full/simulated) properties.

### stdio transport internals

The `stdio://` scheme wraps `os.Stdin` and `os.Stdout` as a single
`net.Conn` delivered through a `net.Listener`. Key design choices:

- **Single connection**: `Accept()` returns exactly one connection, then
  blocks on a `done` channel until the listener is closed. This prevents
  the deadlock that would occur if gRPC's `Serve()` loop called `Accept()`
  a second time on an empty channel.
- **No buffering**: reads and writes go directly to the OS file descriptors.
- **Graceful termination**: closing the connection or the listener signals
  `Accept()` to return `io.EOF`, which makes `grpc.Server.Serve()` exit.

### mem transport

The `mem://` scheme wraps `grpc/test/bufconn` — an in-memory pipe.
The exported `MemListener` provides a `Dial()` method for client-side
connections in the same process. Use cases:

- **Unit testing**: no OS resources needed (no ports, no socket files)
- **Composite holons**: multiple services in one binary calling each other
  at memory speed (~1µs latency)

### WebSocket transport

The `ws://` scheme starts an HTTP server that upgrades WebSocket
connections and presents them as `net.Conn` to the gRPC server. Uses
`nhooyr.io/websocket` for a clean `net.Conn` wrapper via `websocket.NetConn`.

- **Path**: defaults to `/grpc` if omitted (e.g. `ws://:8080` → `ws://:8080/grpc`)
- **Browser access**: the only transport that browsers can use natively
- **NAT traversal**: works through firewalls on port 80/443
- **No proxy needed**: unlike gRPC-Web, this tunnels raw gRPC frames over WS

### WebBridge — embeddable Holon-RPC gateway

`transport.WebBridge` is the **server-side gateway** that makes a Go holon
accessible from browsers over Holon-RPC (JSON-RPC 2.0 + `holon-rpc` subprotocol).

Unlike `holonrpc.Server` (§5), which owns its own TCP listener, WebBridge
**embeds into an existing `http.ServeMux`** — enabling a single HTTP server
to serve static files, REST endpoints, and Holon-RPC simultaneously.

```
┌──────────────┐   WebSocket       ┌──────────────────────────────────┐
│  Browser     │  ws://:8080/rpc   │  Go Holon                        │
│  js-web-     │ ◄──────────────►  │  ┌──────────┐   ┌─────────────┐ │
│  holons      │  holon-rpc sub-   │  │ WebBridge │   │ gRPC server │ │
│  (client)    │  protocol         │  │ (JSON/WS) │   │ (standard)  │ │
└──────────────┘                   │  └──────────┘   └─────────────┘ │
                                   └──────────────────────────────────┘
```

#### API

```go
import "github.com/Organic-Programming/go-holons/pkg/transport"

bridge := transport.NewWebBridge()

// Register handlers for browser → Go calls.
bridge.Register("hello.v1.HelloService/Greet",
    func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
        // ...
        return json.Marshal(result)
    },
)

// Callback when a browser connects — use conn to call browser methods.
bridge.OnConnect(func(conn *transport.WebConn) {
    result, err := conn.InvokeWithTimeout("ui.v1.UIService/GetViewport", nil, 5*time.Second)
})

// CORS restriction (omit for development).
bridge.AllowOrigins("https://example.com")

// Mount on an existing HTTP server.
mux := http.NewServeMux()
mux.HandleFunc("/ws", bridge.HandleWebSocket)
mux.Handle("/", http.FileServer(http.Dir("static")))
http.ListenAndServe(":8080", mux)
```

#### When to use WebBridge vs holonrpc.Server

| | `transport.WebBridge` | `holonrpc.Server` (§5) |
|--|---|---|
| **Integration** | Embeds into existing HTTP server | Standalone (owns listener) |
| **Use case** | Browser-facing holon with static files | Go-to-Go Holon-RPC, interop testing |
| **Handler type** | `func(ctx, json.RawMessage) (json.RawMessage, error)` | `func(ctx, map[string]any) (map[string]any, error)` |
| **Connect event** | `bridge.OnConnect(func(*WebConn))` | `server.WaitForClient(ctx)` |
| **CORS** | `bridge.AllowOrigins(...)` | Not built-in |
| **Wire protocol** | Identical — JSON-RPC 2.0 + `holon-rpc` subprotocol | Identical |

Both follow [PROTOCOL.md §4](../../PROTOCOL.md#4-holon-rpc-binding) exactly.

---

## 3. `pkg/serve` — standard serve command

High-level helper implementing the full `serve` lifecycle. A holon needs
three lines to be fully compliant with Article 11:

```go
import "github.com/Organic-Programming/go-holons/pkg/serve"

case "serve":
    listenURI := serve.ParseFlags(os.Args[2:])
    if err := serve.Run(listenURI, func(s *grpc.Server) {
        pb.RegisterMyServiceServer(s, &myServer{})
    }); err != nil {
        log.Fatal(err)
    }
```

### API

```go
// ParseFlags extracts --listen <URI> or --port <port> from args.
// Returns transport.DefaultURI if neither is present.
func ParseFlags(args []string) string

// Run starts a gRPC server with reflection ON and graceful shutdown.
func Run(listenURI string, register RegisterFunc) error

// RunWithOptions allows disabling reflection (for production/exposed).
func RunWithOptions(listenURI string, register RegisterFunc, reflect bool) error

// RegisterFunc is called to register services on the gRPC server.
type RegisterFunc func(s *grpc.Server)
```

### What `Run` does

1. Calls `transport.Listen(listenURI)` to create the listener.
2. Creates a `grpc.Server`.
3. Calls your `RegisterFunc` to register services.
4. Enables gRPC reflection (Article 2 mandate).
5. Installs signal handlers for `SIGTERM` and `SIGINT` → `GracefulStop()`.
6. Logs the transport and mode.
7. Calls `s.Serve(lis)` — blocks until shutdown.

### Flag parsing

| Flag | Effect |
|------|--------|
| `--listen tcp://:8080` | Use the given transport URI |
| `--listen unix:///tmp/h.sock` | Unix domain socket |
| `--listen stdio://` | stdin/stdout pipe |
| `--listen mem://` | in-process bufconn |
| `--listen ws://:8080` | WebSocket |
| `--port 8080` | Shorthand for `--listen tcp://:8080` |
| *(none)* | Default: `tcp://:9090` |

---

## 4. `pkg/grpcclient` — transport-agnostic gRPC client

Client-side helpers for connecting to holons via any transport.

### API

```go
import "github.com/Organic-Programming/go-holons/pkg/grpcclient"

// Dial connects to an existing gRPC server.
conn, err := grpcclient.Dial(ctx, "localhost:9090")        // TCP
conn, err := grpcclient.Dial(ctx, "unix:///tmp/h.sock")    // Unix socket

// DialStdio launches a holon binary with `serve --listen stdio://`
// and returns a gRPC connection backed by stdin/stdout pipes.
// The caller must close the connection AND kill the process.
conn, cmd, err := grpcclient.DialStdio(ctx, "/path/to/holon")
defer cmd.Process.Kill()
defer cmd.Wait()
defer conn.Close()

// DialMem connects to a MemListener in the same process.
conn, err := grpcclient.DialMem(ctx, memListener)

// DialWebSocket connects via WebSocket.
conn, err := grpcclient.DialWebSocket(ctx, "ws://host:8080/grpc")
```

### DialStdio lifecycle

`DialStdio` is the purest form of inter-holon communication — no port,
no socket file, no network stack. Inspired by LSP.

1. Launch: `exec.CommandContext(ctx, binary, "serve", "--listen", "stdio://")`
2. Capture `cmd.StdinPipe()` (parent writes → child reads) and
   `cmd.StdoutPipe()` (child writes → parent reads).
3. **Readiness probe**: read the first byte from stdout to confirm
   the server's HTTP/2 SETTINGS frame. Prepend it back via `io.MultiReader`.
4. Wrap the pipes as a `net.Conn` (`pipeConn`).
5. `grpc.DialContext` with `passthrough:///` (skip DNS) + `WithBlock`
   (force immediate HTTP/2 handshake) + `WithContextDialer` (single-use dialer).
6. Return `(conn, cmd, nil)`.

The caller owns the process lifecycle. This is intentional — it allows
both ephemeral (call and kill) and long-running (reuse connection) patterns.

### Implementation details

- **`passthrough:///`**: gRPC's default name resolver tries DNS lookup.
  `passthrough` bypasses this for non-network targets.
- **`WithBlock()`**: `grpc.NewClient` defers connection establishment.
  For pipes, we need the HTTP/2 handshake to happen immediately.
- **Single-use dialer**: `sync.Once` ensures the pipe-backed `net.Conn`
  is returned exactly once. gRPC may call the dialer multiple times for
  reconnection; subsequent calls return an error.

---

## 5. `pkg/holonrpc` — Holon-RPC client and server

Implements [PROTOCOL.md §4](../../PROTOCOL.md#4-holon-rpc-binding):
JSON-RPC 2.0 over WebSocket with the `holon-rpc` subprotocol
(mandatory handshake per §4.3). Fully bidirectional — both
client and server can initiate calls (§4.6).

### Client API

```go
import "github.com/Organic-Programming/go-holons/pkg/holonrpc"

client := holonrpc.NewClient()

// Register a handler for server-initiated calls (before or after connect).
client.Register("client.v1.Client/Hello", func(ctx context.Context, params map[string]any) (map[string]any, error) {
    return map[string]any{"greeting": "hello from client"}, nil
})

// Connect (one-shot — no automatic reconnect).
err := client.Connect(ctx, "ws://localhost:8080/rpc")

// Or connect with automatic reconnect (exponential backoff).
err := client.ConnectWithReconnect(ctx, "ws://localhost:8080/rpc")

// Invoke a server-side method.
result, err := client.Invoke(ctx, "echo.v1.Echo/Ping", map[string]any{"message": "hello"})

// Check connection state.
if client.Connected() { /* ... */ }

// Graceful close.
client.Close()
```

### Server API

```go
server := holonrpc.NewServer("ws://127.0.0.1:0/rpc")

// Register handlers for client-originated calls.
server.Register("echo.v1.Echo/Ping", func(ctx context.Context, params map[string]any) (map[string]any, error) {
    return map[string]any{"message": "pong"}, nil
})

// Start listening.
addr, err := server.Start()

// Wait for a client, then call into it (bidirectional).
clientID, _ := server.WaitForClient(ctx)
result, err := server.Invoke(ctx, clientID, "client.v1.Client/Hello", map[string]any{})

// List connected clients.
ids := server.ClientIDs()

// Graceful shutdown.
server.Close(ctx)
```

### Reconnect behavior

When using `ConnectWithReconnect`, the client automatically:

1. Detects disconnection via the read loop.
2. Waits with exponential backoff (500ms → 30s, factor 2.0, 10% jitter).
3. Reconnects and re-registers all handlers.
4. Resumes normal operation — pending `Invoke` calls during disconnection
   fail with `UNAVAILABLE` (code 14); calls after reconnection succeed.

### ID namespacing (PROTOCOL.md §4.6)

- Client-originated IDs: any string or number chosen by the client (convention: `"c1"`, `"c2"`, ...)
- Server-originated IDs: **must** be prefixed with `s` (e.g., `"s1"`, `"s2"`)

### Error codes (PROTOCOL.md §5.2)

| Code | Name | Meaning |
|------|------|---------|
| -32700 | Parse error | Invalid JSON |
| -32600 | Invalid Request | Missing required fields |
| -32601 | Method not found | Unknown method string |
| -32602 | Invalid params | Invalid method parameters |
| -32603 | Internal error | JSON-RPC internal error |
| 14 | UNAVAILABLE | Peer disconnected (gRPC status code, §5.1) |

---

## 6. Agent directives

When creating a new Go holon:

1. **Import `pkg/serve`** for the `serve` subcommand. Do not reimplement
   flag parsing, listener creation, or signal handling.
2. **Import `pkg/transport`** only if you need low-level listener access
   (e.g., custom gRPC server options).
3. **Import `pkg/grpcclient`** when a holon needs to call another holon.
4. **Never duplicate** the transport or stdio plumbing — import this SDK.

When modifying this SDK:

1. **Keep it dependency-light**: only `google.golang.org/grpc`,
   `nhooyr.io/websocket`, and stdlib.
2. **No domain logic**: this is plumbing, not policy.
3. **No proto files**: this SDK has no contract of its own.
4. **Test transports**: any change to `transport` must be verified with
   all six schemes: TCP, Unix, stdio, mem, WebSocket, WebSocket+TLS.

---

## 7. Relationship to the Constitution

| Article | SDK implementation |
|---------|-------------------|
| Article 2 (gRPC + reflection) | `serve.Run` enables reflection by default |
| Article 2 (Holon-RPC binding) | `holonrpc.Client` + `holonrpc.Server` implement §4 |
| Article 11 (serve convention) | `serve.ParseFlags` + `serve.Run` |
| Article 11 (mandatory transports) | `transport.Listen` supports `tcp://` and `stdio://` |
| Article 11 (optional transports) | `transport.Listen` supports `unix://`, `mem://`, `ws://`, `wss://` |
| Article 11 (backward compat) | `serve.ParseFlags` accepts `--port` |
| Article 11 (SIGTERM) | `serve.Run` installs signal handlers |
| Article 11 (bidirectional) | `holonrpc` supports symmetric calls in both directions |
