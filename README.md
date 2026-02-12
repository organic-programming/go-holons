---
# Cartouche v1
title: "go-holons — Go SDK for Organic Programming"
author:
  name: "B. ALTER"
  copyright: "© 2026 Benoit Pereira da Silva"
created: 2026-02-12
revised: 2026-02-12
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
invoke holon methods over WebSocket using a simple JSON envelope protocol.
No third-party wire format (gRPC-Web, Connect) is introduced — the bridge
translates JSON ↔ handler calls entirely in-process.

```
┌──────────────┐   WebSocket       ┌──────────────────────────────────┐
│  Browser     │  ws://:8080/ws    │  Go Holon                        │
│  js-web-     │ ◄──────────────►  │  ┌──────────┐   ┌─────────────┐ │
│  holons      │  holon-web proto  │  │ WebBridge │   │ gRPC server │ │
│  (client)    │                   │  │ (JSON/WS) │   │ (standard)  │ │
└──────────────┘                   │  └──────────┘   └─────────────┘ │
                                   └──────────────────────────────────┘
```

### Usage

```go
bridge := transport.NewWebBridge()

bridge.Register("hello.v1.HelloService/Greet",
    func(ctx context.Context, payload json.RawMessage) (json.RawMessage, error) {
        var req struct { Name string `json:"name"` }
        json.Unmarshal(payload, &req)
        return json.Marshal(map[string]string{"message": "Hello, " + req.Name + "!"})
    },
)

mux := http.NewServeMux()
mux.HandleFunc("/ws", bridge.HandleWebSocket)
http.ListenAndServe(":8080", mux)
```

### Wire Protocol

| Direction | Format |
|-----------|--------|
| Browser → Server | `{ "id": "1", "method": "pkg.Service/Method", "payload": {...} }` |
| Server → Browser (success) | `{ "id": "1", "result": {...} }` |
| Server → Browser (error) | `{ "id": "1", "error": { "code": 12, "message": "..." } }` |

See [AGENT.md](./AGENT.md) for full documentation.
