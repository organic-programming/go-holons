package holonrpc_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"

	"github.com/Organic-Programming/go-holons/pkg/holonrpc"
)

func startHolonRPCServer(t *testing.T, register func(*holonrpc.Server)) (*holonrpc.Server, string) {
	t.Helper()

	server := holonrpc.NewServer("ws://127.0.0.1:0/rpc")
	if register != nil {
		register(server)
	}

	addr, err := server.Start()
	if err != nil {
		t.Fatalf("start server: %v", err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})

	return server, addr
}

func dialRawHolonRPC(t *testing.T, addr string) *websocket.Conn {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ws, _, err := websocket.Dial(ctx, addr, &websocket.DialOptions{
		Subprotocols: []string{"holon-rpc"},
	})
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	return ws
}

func connectHolonRPCClient(t *testing.T, addr string) *holonrpc.Client {
	t.Helper()

	client := holonrpc.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Connect(ctx, addr); err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
	})
	return client
}

func readWSJSONMap(t *testing.T, ws *websocket.Conn, timeout time.Duration) map[string]any {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	_, data, err := ws.Read(ctx)
	if err != nil {
		t.Fatalf("read websocket: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return out
}

func requireRPCErrorCode(t *testing.T, err error, want int) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected RPC error code %d, got nil", want)
	}

	var rpcErr *holonrpc.ResponseError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("expected ResponseError, got %T: %v", err, err)
	}
	if rpcErr.Code != want {
		t.Fatalf("error code = %d, want %d", rpcErr.Code, want)
	}
}

func requireWireErrorCode(t *testing.T, msg map[string]any, want int) {
	t.Helper()
	errObj, ok := msg["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error object, got: %#v", msg)
	}
	got, ok := errObj["code"].(float64)
	if !ok {
		t.Fatalf("expected numeric error code, got: %#v", errObj["code"])
	}
	if int(got) != want {
		t.Fatalf("error code = %v, want %d", got, want)
	}
}

func TestHolonRPCGoAgainstGoDirectional(t *testing.T) {
	server, addr := startHolonRPCServer(t, func(s *holonrpc.Server) {
		s.Register("echo.v1.Echo/Ping", func(_ context.Context, params map[string]any) (map[string]any, error) {
			return params, nil
		})
	})

	client := holonrpc.NewClient()
	client.Register("client.v1.Client/Hello", func(_ context.Context, params map[string]any) (map[string]any, error) {
		name, _ := params["name"].(string)
		return map[string]any{"message": fmt.Sprintf("hello %s", name)}, nil
	})
	t.Cleanup(func() {
		_ = client.Close()
	})

	connectCtx, cancelConnect := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelConnect()
	if err := client.Connect(connectCtx, addr); err != nil {
		t.Fatalf("connect client: %v", err)
	}

	clientIDCtx, cancelClientID := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelClientID()
	clientID, err := server.WaitForClient(clientIDCtx)
	if err != nil {
		t.Fatalf("wait for client: %v", err)
	}

	invokeCtx, cancelInvoke := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelInvoke()
	echo, err := client.Invoke(invokeCtx, "echo.v1.Echo/Ping", map[string]any{"message": "hello"})
	if err != nil {
		t.Fatalf("client invoke echo: %v", err)
	}
	if got, _ := echo["message"].(string); got != "hello" {
		t.Fatalf("echo message = %q, want %q", got, "hello")
	}

	reply, err := server.Invoke(invokeCtx, clientID, "client.v1.Client/Hello", map[string]any{"name": "go"})
	if err != nil {
		t.Fatalf("server invoke client: %v", err)
	}
	if got, _ := reply["message"].(string); got != "hello go" {
		t.Fatalf("client reply message = %q, want %q", got, "hello go")
	}
}

func TestHolonRPCClientRejectsNonDirectionalID(t *testing.T) {
	respCh := make(chan map[string]any, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols:       []string{"holon-rpc"},
			InsecureSkipVerify: true,
		})
		if err != nil {
			return
		}
		defer c.CloseNow()

		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		req := map[string]any{
			"jsonrpc": "2.0",
			"id":      "c99",
			"method":  "client.v1.Client/Hello",
			"params":  map[string]any{"name": "go"},
		}
		reqData, _ := json.Marshal(req)
		if err := c.Write(ctx, websocket.MessageText, reqData); err != nil {
			return
		}

		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		var resp map[string]any
		if err := json.Unmarshal(data, &resp); err != nil {
			return
		}
		respCh <- resp
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	client := holonrpc.NewClient()
	client.Register("client.v1.Client/Hello", func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return map[string]any{"message": "unexpected"}, nil
	})
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Connect(ctx, wsURL); err != nil {
		t.Fatalf("connect client: %v", err)
	}

	select {
	case resp := <-respCh:
		requireWireErrorCode(t, resp, -32600)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for rejection response")
	}
}

func TestHolonRPCHeartbeat(t *testing.T) {
	_, addr := startHolonRPCServer(t, nil)
	client := connectHolonRPCClient(t, addr)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := client.Invoke(ctx, "rpc.heartbeat", nil)
	if err != nil {
		t.Fatalf("heartbeat invoke: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("heartbeat result = %#v, want empty object", result)
	}
}

func TestHolonRPCNotification(t *testing.T) {
	called := make(chan struct{}, 1)
	_, addr := startHolonRPCServer(t, func(s *holonrpc.Server) {
		s.Register("notify.v1.Notify/Send", func(_ context.Context, params map[string]any) (map[string]any, error) {
			if params["value"] == "x" {
				called <- struct{}{}
			}
			return map[string]any{"ok": true}, nil
		})
	})

	ws := dialRawHolonRPC(t, addr)
	defer ws.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req := []byte(`{"jsonrpc":"2.0","method":"notify.v1.Notify/Send","params":{"value":"x"}}`)
	if err := ws.Write(ctx, websocket.MessageText, req); err != nil {
		t.Fatalf("write notification: %v", err)
	}

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("notification handler was not called")
	}

	readCtx, cancelRead := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancelRead()
	_, _, err := ws.Read(readCtx)
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "closed network connection") {
		t.Fatalf("expected no response for notification, got: %v", err)
	}
}

func TestHolonRPCUnknownMethod(t *testing.T) {
	_, addr := startHolonRPCServer(t, nil)
	client := connectHolonRPCClient(t, addr)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := client.Invoke(ctx, "does.not.Exist/Method", map[string]any{})
	requireRPCErrorCode(t, err, -32601)
}

func TestHolonRPCInvalidParams(t *testing.T) {
	_, addr := startHolonRPCServer(t, func(s *holonrpc.Server) {
		s.Register("echo.v1.Echo/Ping", func(_ context.Context, params map[string]any) (map[string]any, error) {
			return params, nil
		})
	})

	ws := dialRawHolonRPC(t, addr)
	defer ws.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req := []byte(`{"jsonrpc":"2.0","id":"badparams","method":"echo.v1.Echo/Ping","params":"a-string"}`)
	if err := ws.Write(ctx, websocket.MessageText, req); err != nil {
		t.Fatalf("write request: %v", err)
	}

	resp := readWSJSONMap(t, ws, 2*time.Second)
	requireWireErrorCode(t, resp, -32602)
}

func TestHolonRPCParseError(t *testing.T) {
	_, addr := startHolonRPCServer(t, nil)
	ws := dialRawHolonRPC(t, addr)
	defer ws.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := ws.Write(ctx, websocket.MessageText, []byte(`{bad-json`)); err != nil {
		t.Fatalf("write malformed json: %v", err)
	}

	resp := readWSJSONMap(t, ws, 2*time.Second)
	requireWireErrorCode(t, resp, -32700)
}

func TestHolonRPCServerUnregister(t *testing.T) {
	_, addr := startHolonRPCServer(t, func(s *holonrpc.Server) {
		s.Register("echo.v1.Echo/Ping", func(_ context.Context, params map[string]any) (map[string]any, error) {
			return params, nil
		})
		s.Unregister("echo.v1.Echo/Ping")
	})

	client := connectHolonRPCClient(t, addr)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := client.Invoke(ctx, "echo.v1.Echo/Ping", map[string]any{"message": "x"})
	requireRPCErrorCode(t, err, -32601)
}

func TestHolonRPCClientConnected(t *testing.T) {
	_, addr := startHolonRPCServer(t, nil)
	client := holonrpc.NewClient()

	if client.Connected() {
		t.Fatal("Connected() = true before Connect, want false")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Connect(ctx, addr); err != nil {
		t.Fatalf("connect client: %v", err)
	}
	if !client.Connected() {
		t.Fatal("Connected() = false after Connect, want true")
	}

	if err := client.Close(); err != nil {
		t.Fatalf("close client: %v", err)
	}
	if client.Connected() {
		t.Fatal("Connected() = true after Close, want false")
	}
}

func TestHolonRPCDoubleClose(t *testing.T) {
	_, addr := startHolonRPCServer(t, nil)
	client := holonrpc.NewClient()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Connect(ctx, addr); err != nil {
		t.Fatalf("connect client: %v", err)
	}

	if err := client.Close(); err != nil {
		t.Fatalf("first close failed: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("second close failed: %v", err)
	}
}
