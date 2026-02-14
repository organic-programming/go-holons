---
# Cartouche v1
title: "HOWTO — go-holons Integration Guide"
author:
  name: "Antigravity"
  copyright: "© 2026 Benoit Pereira da Silva"
created: 2026-02-14
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
# HOWTO — go-holons Integration Guide

Concrete integration patterns for developers and agents building Go holons.
Each section is self-contained with copy-pastable code.

> **Import path**: `github.com/Organic-Programming/go-holons/pkg/...`

---

## 1. Serve a gRPC Holon (Standard Pattern)

Every Go holon follows the same `serve --listen <URI>` convention.
The `serve` package handles signal trapping, graceful shutdown (10s drain),
and gRPC reflection.

```go
package main

import (
    "os"

    pb "your-holon/gen/proto"
    "github.com/Organic-Programming/go-holons/pkg/serve"
    "google.golang.org/grpc"
)

func main() {
    listenURI := serve.ParseFlags(os.Args[1:])
    serve.Run(listenURI, func(s *grpc.Server) {
        pb.RegisterMyServiceServer(s, &myServer{})
    })
}
```

```bash
# TCP on random port
go run . --listen tcp://127.0.0.1:0

# Unix domain socket
go run . --listen unix:///tmp/my-holon.sock

# stdin/stdout pipe (for DialStdio callers)
go run . --listen stdio://
```

---

## 2. Connect to a Remote Holon (TCP / Unix)

```go
import "github.com/Organic-Programming/go-holons/pkg/grpcclient"

// TCP
conn, err := grpcclient.Dial(ctx, "localhost:9090")
defer conn.Close()

// Unix socket
conn, err := grpcclient.Dial(ctx, "unix:///tmp/my-holon.sock")
defer conn.Close()

// Use the connection like any gRPC client
client := pb.NewMyServiceClient(conn)
resp, err := client.DoSomething(ctx, &pb.Request{...})
```

---

## 3. Spawn and Connect to a Binary (stdio)

Fork a holon binary and talk gRPC over pipes — no port, no socket, no network.
The binary must support `serve --listen stdio://`.

```go
import "github.com/Organic-Programming/go-holons/pkg/grpcclient"

conn, cmd, err := grpcclient.DialStdio(ctx, "/path/to/holon-binary")
if err != nil {
    log.Fatal(err)
}
defer cmd.Process.Kill()
defer conn.Close()

// Standard gRPC from here on
client := pb.NewMyServiceClient(conn)
resp, err := client.DoSomething(ctx, &pb.Request{...})
```

**How it works**: `DialStdio` runs the binary with `serve --listen stdio://`,
captures its stdin/stdout as pipes, waits for the HTTP/2 SETTINGS frame to
confirm the server is alive, then wraps the pipes as a `net.Conn` for gRPC.

---

## 4. In-Process Composition (mem://)

Run server and client in the same process — ideal for composite holons and tests.

```go
import (
    "github.com/Organic-Programming/go-holons/pkg/transport"
    "google.golang.org/grpc"
    "google.golang.org/grpc/credentials/insecure"
)

// Server side
lis := transport.NewMemListener()
s := grpc.NewServer()
pb.RegisterMyServiceServer(s, &myServer{})
go s.Serve(lis)

// Client side (same process)
conn, err := grpc.NewClient(
    "passthrough:///mem",
    grpc.WithTransportCredentials(insecure.NewCredentials()),
    grpc.WithContextDialer(lis.DialContext),
)
client := pb.NewMyServiceClient(conn)
```

---

## 5. gRPC over WebSocket

Tunnel standard gRPC through a WebSocket — useful for NAT traversal
and browser-adjacent infrastructure.

### Server side

```go
// transport.Listen handles ws:// natively
lis, err := transport.Listen("ws://0.0.0.0:8443/grpc")
s := grpc.NewServer()
pb.RegisterMyServiceServer(s, &myServer{})
s.Serve(lis)
```

### Client side

```go
import "github.com/Organic-Programming/go-holons/pkg/grpcclient"

conn, err := grpcclient.DialWebSocket(ctx, "ws://192.168.1.42:8443/grpc")
defer conn.Close()

client := pb.NewMyServiceClient(conn)
```

> The WebSocket subprotocol is `"grpc"` (binary HTTP/2 frames), distinct
> from `"holon-rpc"` (JSON-RPC 2.0 text frames).

---

## 6. Holon-RPC: Go ↔ Go (Standalone Server)

Bidirectional JSON-RPC 2.0 over WebSocket without gRPC.
Use this when both sides are Go but you want the Holon-RPC wire format
(e.g. for protocol conformance testing or mixed-language topologies).

### Server

```go
import "github.com/Organic-Programming/go-holons/pkg/holonrpc"

server := holonrpc.NewServer("ws://127.0.0.1:0/rpc")

// Handle client → server calls
server.Register("echo.v1.Echo/Ping", func(ctx context.Context, params map[string]any) (map[string]any, error) {
    return map[string]any{"message": params["message"]}, nil
})

addr, err := server.Start()  // addr = "ws://127.0.0.1:<port>/rpc"
defer server.Close(context.Background())
```

### Client

```go
import "github.com/Organic-Programming/go-holons/pkg/holonrpc"

client := holonrpc.NewClient()

// Handle server → client calls (bidirectional)
client.Register("client.v1.Client/Info", func(ctx context.Context, params map[string]any) (map[string]any, error) {
    return map[string]any{"name": "my-agent", "version": "1.0"}, nil
})

err := client.Connect(ctx, "ws://127.0.0.1:8080/rpc")
defer client.Close()

// Invoke a server method
result, err := client.Invoke(ctx, "echo.v1.Echo/Ping", map[string]any{
    "message": "hello",
})
// result = {"message": "hello"}
```

### Auto-reconnect

```go
// Exponential backoff: 500ms → 30s, factor 2.0, jitter 0.1
err := client.ConnectWithReconnect(ctx, "ws://127.0.0.1:8080/rpc")
```

### Server-initiated call to a specific client

```go
clientID, err := server.WaitForClient(ctx) // blocks until a client connects
result, err := server.Invoke(ctx, clientID, "client.v1.Client/Info", nil)
```

---

## 7. WebBridge: Browser ↔ Go (Embedded Gateway)

Mount Holon-RPC alongside static files on an existing `http.ServeMux`.
This is the standard pattern for browser-facing holons.

```go
import "github.com/Organic-Programming/go-holons/pkg/transport"

bridge := transport.NewWebBridge()

// Browser → Go
bridge.Register("hello.v1.HelloService/Greet", func(_ context.Context, payload json.RawMessage) (json.RawMessage, error) {
    var req struct{ Name string `json:"name"` }
    json.Unmarshal(payload, &req)
    return json.Marshal(map[string]string{
        "message": fmt.Sprintf("Hello, %s!", req.Name),
    })
})

// Go → Browser (called when a browser connects)
bridge.OnConnect(func(conn *transport.WebConn) {
    result, err := conn.InvokeWithTimeout("ui.v1.UIService/GetViewport", nil, 5*time.Second)
    // result contains the browser's viewport dimensions
})

mux := http.NewServeMux()
mux.HandleFunc("/ws", bridge.HandleWebSocket)
mux.Handle("/", http.FileServer(http.Dir("static")))

http.ListenAndServe(":8080", mux)
```

### Browser side (using js-web-holons)

```javascript
import { HolonRPCClient } from "js-web-holons";

const client = new HolonRPCClient("ws://localhost:8080/ws");

// Register browser-side handler for Go → Browser calls
client.register("ui.v1.UIService/GetViewport", () => ({
    width: window.innerWidth,
    height: window.innerHeight,
    devicePixelRatio: window.devicePixelRatio,
}));

await client.connect();

// Browser → Go call
const result = await client.invoke("hello.v1.HelloService/Greet", { name: "Alice" });
// result = { message: "Hello, Alice!" }
```

---

## Quick Reference

| I want to… | Package | Function |
|---|---|---|
| Serve gRPC with signal handling | `serve` | `serve.Run(uri, register)` |
| Connect to TCP/Unix gRPC | `grpcclient` | `Dial(ctx, addr)` |
| Spawn and connect to a binary | `grpcclient` | `DialStdio(ctx, path)` |
| Connect via WebSocket | `grpcclient` | `DialWebSocket(ctx, uri)` |
| In-process gRPC (tests) | `transport` | `NewMemListener()` + `DialContext` |
| Listen on any URI | `transport` | `Listen(uri)` |
| Holon-RPC server (standalone) | `holonrpc` | `NewServer(url)` |
| Holon-RPC client | `holonrpc` | `NewClient()` + `Connect(ctx, url)` |
| Browser gateway (embeddable) | `transport` | `NewWebBridge()` |
| Go → Browser call | `transport` | `WebConn.Invoke(ctx, method, payload)` |

### Transport URI Cheatsheet

| URI | Direction | Use case |
|-----|-----------|----------|
| `tcp://host:port` | Listen / Dial | Classic gRPC |
| `unix:///path` | Listen / Dial | Local IPC, no TCP overhead |
| `stdio://` | Listen / Dial | Fork+exec child holon (LSP-style) |
| `mem://` | Listen / Dial | In-process (tests, composite holons) |
| `ws://host:port/path` | Listen / Dial | gRPC through WebSocket |
| `wss://host:port/path` | Listen / Dial | gRPC through WebSocket + TLS |
