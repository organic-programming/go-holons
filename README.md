---
# Cartouche v1
title: "go-holons — Go SDK for Organic Programming"
author:
  name: "B. ALTER"
  copyright: "© 2026 Benoit Pereira da Silva"
created: 2026-02-12
revised: 2026-02-14
lang: en-US
origin_lang: en-US
translation_of: null
translator: null
access:
  humans: true
  agents: false
status: draft
---
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
| `holonrpc` | `go-holons/pkg/holonrpc` | Holon-RPC client + server (JSON-RPC 2.0 over WebSocket) |

## Transports

| Scheme | Description |
|--------|-------------|
| `tcp://<host>:<port>` | TCP socket, classic gRPC |
| `unix://<path>` | Unix domain socket, local IPC |
| `stdio://` | stdin/stdout pipe (LSP-style) |
| `mem://` | In-process bufconn (testing, composite holons) |
| `ws://<host>:<port>` | WebSocket (browser, NAT traversal) |
| `wss://<host>:<port>` | WebSocket over TLS |

## WebBridge (Browser Gateway)

The `transport.WebBridge` is a Go-only feature that lets browser clients
communicate with a holon over WebSocket using Holon-RPC (JSON-RPC 2.0,
`holon-rpc` subprotocol). Calls are **bidirectional** — the browser can
call holon methods, and the holon can call methods registered in the
browser. No third-party wire format (gRPC-Web, Connect) is introduced —
the bridge translates JSON ↔ handler calls entirely in-process.

```
┌──────────────┐   WebSocket       ┌──────────────────────────────────┐
│  Browser     │  ws://:8080/ws    │  Go Holon                        │
│  js-web-     │ ◄──────────────►  │  ┌──────────┐   ┌─────────────┐ │
│  holons      │  holon-rpc proto  │  │ WebBridge │   │ gRPC server │ │
│  (client)    │                   │  │ (JSON/WS) │   │ (standard)  │ │
└──────────────┘                   │  └──────────┘   └─────────────┘ │
                                   └──────────────────────────────────┘
```

### Usage

```go
bridge := transport.NewWebBridge()

bridge.Register("hello.v1.HelloService/Greet",
    func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
        var req struct { Name string `json:"name"` }
        json.Unmarshal(params, &req)
        return json.Marshal(map[string]string{"message": "Hello, " + req.Name + "!"})
    },
)

mux := http.NewServeMux()
mux.HandleFunc("/ws", bridge.HandleWebSocket)
http.ListenAndServe(":8080", mux)
```

### Wire Protocol (Holon-RPC)

| Direction | Format |
|-----------|--------|
| Client → Server | `{ "jsonrpc":"2.0", "id":"c1", "method":"pkg.Service/Method", "params": {...} }` |
| Server → Client (response) | `{ "jsonrpc":"2.0", "id":"c1", "result": {...} }` |
| Server → Client (error) | `{ "jsonrpc":"2.0", "id":"c1", "error": { "code": -32601, "message": "..." } }` |
| Server → Client (call) | `{ "jsonrpc":"2.0", "id":"s1", "method":"client.v1/Info", "params": {...} }` |
| Client → Server (response) | `{ "jsonrpc":"2.0", "id":"s1", "result": {...} }` |

Server-originated IDs use the `s` prefix per PROTOCOL.md §4.6.

## Quality Gates

Run the full production robustness suite with race detection:

```bash
cd sdk/go-holons
go test ./... -v -count=1 -race
```

Coverage targets (minimum 80% for each package):

```bash
go test -coverprofile=c.out ./pkg/transport/ && go tool cover -func=c.out
go test -coverprofile=c.out ./pkg/holonrpc/ && go tool cover -func=c.out
go test -coverprofile=c.out ./pkg/serve/ && go tool cover -func=c.out
go test -coverprofile=c.out ./pkg/grpcclient/ && go tool cover -func=c.out
```

Static analysis:

```bash
go vet ./...
```

See [AGENT.md](./AGENT.md) for full documentation.
