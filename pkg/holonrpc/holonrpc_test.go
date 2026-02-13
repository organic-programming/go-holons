package holonrpc_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"

	"github.com/Organic-Programming/go-holons/pkg/holonrpc"
)

func TestHolonRPCGoAgainstGoDirectional(t *testing.T) {
	server := holonrpc.NewServer("ws://127.0.0.1:0/rpc")
	server.Register("echo.v1.Echo/Ping", func(_ context.Context, params map[string]any) (map[string]any, error) {
		return params, nil
	})

	addr, err := server.Start()
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Close(ctx)
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
		errObj, ok := resp["error"].(map[string]any)
		if !ok {
			t.Fatalf("response error object missing: %#v", resp)
		}
		if code, _ := errObj["code"].(float64); int(code) != -32600 {
			t.Fatalf("error code = %v, want -32600", errObj["code"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for rejection response")
	}
}
