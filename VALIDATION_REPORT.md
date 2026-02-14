# Go SDK Validation Report

## Scope
Validated and hardened `sdk/go-holons/` against transport, gRPC client/server, Holon-RPC, echo CLI, race safety, and protocol compliance targets.

Primary verification command used after each edit set:

```bash
GOROOT=/opt/homebrew/opt/go/libexec go test ./... -count=1 -race
```

Final result: **PASS**.

## Bugs Found And Fixed

1. Unix transport leaked socket paths after close.
- Fix: wrapped unix listeners with cleanup-on-close behavior in `pkg/transport/transport.go`.
- Regression: `TestTransport_UnixSocketCleanupOnClose`.

2. WebSocket URI parsing mishandled query/path edge cases.
- Fix: moved WS/WSS parsing to `net/url` with explicit host:port validation and query isolation in `pkg/transport/ws.go`.
- Regression: `TestTransport_URIParsingEdgeCases`.

3. `wss://` listener path was non-functional (ServeTLS called without cert/key).
- Fix: added explicit certificate/key requirements via URI query (`cert`, `key`) or env vars (`HOLONS_WSS_CERT_FILE`, `HOLONS_WSS_KEY_FILE`) in `pkg/transport/ws.go`.
- Regression: `TestTransport_WSS_RoundTrip`.

4. Holon-RPC decode errors incorrectly mapped valid-but-invalid envelopes to parse errors.
- Fix: classify malformed JSON as `-32700`, valid JSON with invalid envelope shape (including batch arrays) as `-32600`.
- Files: `pkg/holonrpc/types.go`, `pkg/holonrpc/client.go`, `pkg/holonrpc/server.go`.
- Regression: `TestHolonRPCInvalidRequestEnvelopeShape`, `TestHolonRPCBatchRequestUnsupported`.

5. Holon-RPC internal handler failures used gRPC code `13` instead of JSON-RPC `-32603`.
- Fix: map unexpected internal handler/runtime failures to `-32603` in client/server request handlers.
- Regression: `TestHolonRPCInternalErrorCode`.

6. WebBridge (`transport/wsweb`) had the same protocol error-code mismatches.
- Fix: aligned parse/invalid-request/internal-error mapping with JSON-RPC 2.0.
- Updated tests accordingly in `pkg/transport/wsweb_test.go`.

7. gRPC-over-WS client could fail permanently with `ws connection already consumed` on retry/reconnect paths.
- Fix: `grpcclient.DialWebSocket` now opens a fresh WebSocket inside the gRPC context dialer instead of reusing a single pre-opened connection.
- File: `pkg/grpcclient/ws.go`.

8. Echo server lacked compatibility with `grpcclient.DialStdio` process contract (`serve --listen stdio://`).
- Fix: added `serve` subcommand-arg compatibility in `cmd/echo-server/main.go`.

9. Echo client lacked direct `stdio://` transport support.
- Fix: added stdio URI dispatch with child-process stdio dialing in `cmd/echo-client/main.go`.
- Regression: `TestEchoClient_Stdio_RoundTrip`.

## Tests Added/Expanded

### Transport (`pkg/transport`)
- `TestTransport_TCP_RoundTrip`
- `TestTransport_Unix_RoundTrip`
- `TestTransport_Stdio_RoundTrip`
- `TestTransport_WSS_RoundTrip`
- `TestTransport_URIParsingEdgeCases`
- `TestTransport_UnixSocketCleanupOnClose`
- `TestTransport_ConcurrentDialSameAddress`
- `TestTransport_ListenerCloseWithActiveConnections`

### gRPC client (`pkg/grpcclient`)
- Extended existing round-trip tests to include stream coverage over TCP/Unix/Mem/WS/Stdio.
- `TestDialUnary_ContextCancellation`
- `TestDialUnary_DeadlinePropagation`
- `TestDial_ReconnectAfterServerRestart`
- `TestDial_NonExistentServerReturnsUnavailable`

### gRPC serve wrapper (`pkg/serve`)
- `TestRunServesGRPCOnUnixSocket`
- `TestRunServesGRPCOnWebSocket`
- `TestRunGracefulShutdownWithInFlightRPC`
- `TestRunConcurrentClients`
- `TestRunMetadataHeadersAndTrailers`
- `TestRunRejectsOversizedMessage`
- Expanded stream checks in existing run test.

### Holon-RPC (`pkg/holonrpc`)
- `TestHolonRPCInvalidRequestEnvelopeShape`
- `TestHolonRPCBatchRequestUnsupported`
- `TestHolonRPCInternalErrorCode`
- `TestHolonRPC_MessageOrderingByRequestID`
- `TestHolonRPC_LargePayloadNearLimit`
- `TestHolonRPC_OversizedMessageRejection`

### Echo CLI (`cmd/echo-server`, `cmd/echo-client`)
- Added full command package test suites including helper-process execution and end-to-end checks.
- Includes tcp/unix/stdio client↔server round-trips.

## Coverage Before/After

| Package | Before | After |
|---|---:|---:|
| `cmd/echo-client` | 0.0% | 85.1% |
| `cmd/echo-server` | 0.0% | 82.7% |
| `pkg/grpcclient` | 83.8% | 86.8% |
| `pkg/holonrpc` | 81.5% | 81.8% |
| `pkg/serve` | 90.6% | 90.6% |
| `pkg/transport` | 86.2% | 84.9% |

All current packages are **>= 80%**.

## Final Verification

```bash
GOROOT=/opt/homebrew/opt/go/libexec go test ./... -count=1 -race
```

Output: all packages passed.

## Notes

- `wss://` certificate/key provisioning is intentional by design (no runtime auto-generation) and is documented in `README.md` under **WSS Configuration**.
