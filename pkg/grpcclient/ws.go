package grpcclient

import (
	"context"
	"fmt"
	"net"

	"nhooyr.io/websocket"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// DialWebSocket connects to a gRPC server via WebSocket.
// uri is "ws://host:port/path" or "wss://host:port/path".
// The path defaults to "/grpc" if omitted.
//
// It intentionally negotiates the WebSocket subprotocol "grpc", not
// "holon-rpc": this transport carries raw gRPC HTTP/2 bytes inside binary
// WebSocket frames, while holon-rpc is JSON-RPC 2.0 over text frames.
func DialWebSocket(ctx context.Context, uri string) (*grpc.ClientConn, error) {
	// Connect WebSocket
	c, _, err := websocket.Dial(ctx, uri, &websocket.DialOptions{
		Subprotocols: []string{"grpc"},
	})
	if err != nil {
		return nil, fmt.Errorf("websocket dial %s: %w", uri, err)
	}

	// Wrap as net.Conn
	wsConn := websocket.NetConn(ctx, c, websocket.MessageBinary)

	// Single-use dialer — the WebSocket connection is already established
	dialed := false
	dialer := func(_ context.Context, _ string) (net.Conn, error) {
		if dialed {
			return nil, fmt.Errorf("ws connection already consumed")
		}
		dialed = true
		return wsConn, nil
	}

	//nolint:staticcheck // DialContext is deprecated but needed for single-connection transports.
	conn, err := grpc.DialContext(ctx,
		"passthrough:///ws",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(dialer),
		grpc.WithBlock(),
	)
	if err != nil {
		wsConn.Close()
		return nil, fmt.Errorf("grpc handshake over ws: %w", err)
	}

	return conn, nil
}
