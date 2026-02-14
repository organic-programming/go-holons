# Go SDK Validation Report

Date: 2026-02-14
Scope: `sdk/go-holons/` reference implementation hardening and validation
Final command: `go test ./... -count=1 -race -coverprofile=coverage.out`

## 1. Bugs Found and Fixed

### Bug A: WebBridge dropped invalid request envelopes with `id` but missing `method`
- File: `pkg/transport/wsweb.go`
- Impact: JSON-RPC invalid requests could be silently ignored instead of returning `-32600`.

Before:
```go
if msg.Method != "" {
    go b.handleRequest(ctx, conn, msg)
} else {
    conn.handleResponse(msg)
}
```

After:
```go
if msg.Method != "" {
    go b.handleRequest(ctx, conn, msg)
} else if len(msg.Result) > 0 || msg.Error != nil {
    conn.handleResponse(msg)
} else if strings.TrimSpace(msg.ID) != "" {
    writeInvalidRequest(-32600)
}
```

Regression test:
- `TestWebBridgeInvalidRequestMissingMethod`

### Bug B: Holon-RPC multivalent dispatch routing (§4.9) was missing in `pkg/holonrpc`
- Files: `pkg/holonrpc/routing.go`, `pkg/holonrpc/server.go`
- Impact: server supported point-to-point bidirectional calls only; no fan-out, broadcast-response, full-broadcast, or routing-field stripping.

Before:
```go
params, _ := decodeParams(msg.Params)
handler := s.handlers[method]
result, _ := s.callPeerHandler(peer.ctx, handler, params)
_ = s.sendPeerResult(peer, reqID, result)
```

After:
```go
dispatchMethod, fanOut, cleanedParams, hints, _ := parseRouteHints(method, params)
routed, _ := s.routePeerRequest(peer, reqID, dispatchMethod, cleanedParams, hints, fanOut)
if routed { return }
// fallback: local handler path
```

Implemented routing features:
- wildcard method parsing (`*.Echo/Ping` -> fan-out dispatch)
- fan-out parallel dispatch + aggregated array response
- `_routing` parsing (`broadcast-response`, `full-broadcast`) and stripping before handler dispatch
- `_peer` unicast target hint for peer-ID routing
- notification broadcast helper for response propagation
- no global lock held while waiting fan-out responses (re-entrancy safe)

## 2. Tests Added

### Tier 1 (functional/protocol)
- `TestWebBridgeInvalidRequestMissingMethod`
- `TestWSListenerAcceptAfterCloseReturnsEOF`
- `TestHolonRPCInvalidRequestMissingMethod`
- `TestHolonRPCMethodNotFoundErrorContainsMethodName`
- `TestHolonRPCClientRejectsMissingSubprotocolNegotiation`
- `TestHolonRPCNullIDNotification`
- `TestEchoServiceDescHandler_InterceptorAndDecodeError`

### Tier 1.3 Routing (§4.9) new coverage
- `TestHolonRPC_Routing_Unicast_TargetPeerID`
- `TestHolonRPC_Routing_FanOut_AggregatesResults`
- `TestHolonRPC_Routing_FanOut_PartialFailure`
- `TestHolonRPC_Routing_FanOut_EmptyReturnsNotFound`
- `TestHolonRPC_Routing_BroadcastResponse`
- `TestHolonRPC_Routing_FullBroadcast`
- `TestHolonRPC_Routing_ConcurrentFanOut`
- `TestHolonRPC_Routing_Unicast_UnknownPeerReturnsNotFound`

### Tier 2+ (resilience/branch hardening)
- `TestShutdown_ForceStopAfterTimeout`
- `TestMain_GracefulSignalPath`
- `TestMain_ServeAliasSignalPath`
- `TestReconnectDelayBounds`
- `TestDisableReconnectMismatchedDoneIsNoop`
- `TestServerStartDefaultsIdempotentAndClosed`
- `TestServerInvokeUnknownClient`
- `TestClientHandleResponseInvalidVersion`
- `TestServerHandlePeerResponseInvalidVersion`
- `TestClientReadLoopInvalidEnvelopeWithID`
- `TestClientReadLoopIgnoresBinaryFrames`
- `TestClientSendResultMarshalFailureReturnsInternalError`
- `TestServerSendPeerResultMarshalFailureReturnsInternalError`
- `TestSendPeerErrorMarshalFailure`

## 3. Coverage Per Package (Before/After)

Before = initial baseline run in this hardening session.
After = final run of `go test ./... -count=1 -race -coverprofile=coverage.out`.

| Package | Before | After | Delta |
|---|---:|---:|---:|
| `cmd/echo-client` | 85.1% | 85.1% | +0.0 |
| `cmd/echo-server` | 82.7% | 90.1% | +7.4 |
| `pkg/grpcclient` | 86.8% | 86.8% | +0.0 |
| `pkg/holonrpc` | 81.8% | 85.3% | +3.5 |
| `pkg/serve` | 90.6% | 90.6% | +0.0 |
| `pkg/transport` | 84.5% | 85.5% | +1.0 |

All packages are now >= 85%.

## 4. Final Validation Run

Command:
```bash
go test ./... -count=1 -race -coverprofile=coverage.out
```

Result: PASS

- `cmd/echo-client`: 85.1%
- `cmd/echo-server`: 90.1%
- `pkg/grpcclient`: 86.8%
- `pkg/holonrpc`: 85.3%
- `pkg/serve`: 90.6%
- `pkg/transport`: 85.5%

## 5. Remaining Issues / Known Limitations

1. Unicast peer targeting for bridge routing is exposed through the `_peer` params hint (bridge convention) because PROTOCOL §4.9 defines topologies semantically but does not define an explicit wire field for peer-ID addressing.
2. Fan-out currently dispatches to all connected peers (excluding caller), matching the “or all peers if discovery isn't available” fallback; capability-registry-based target filtering (`rpc.discover`) is not yet wired in.

## 6. Total Test Count and Pass Rate

- Top-level `Test*` functions: 146 (includes 2 `TestMain` functions)
- Runnable tests: 144
- Pass rate: 100% (144/144 runnable tests passed)
