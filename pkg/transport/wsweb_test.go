package transport_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"

	"github.com/Organic-Programming/go-holons/pkg/transport"
)

// helper: dial a test server and return the WebSocket conn
func dialTestBridge(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	ctx := context.Background()
	wsURL := "ws" + srv.URL[4:]
	c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		Subprotocols: []string{"holon-web"},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return c
}

// helper: send a request and read the response
func roundTrip(t *testing.T, c *websocket.Conn, req string) map[string]interface{} {
	t.Helper()
	ctx := context.Background()
	if err := c.Write(ctx, websocket.MessageText, []byte(req)); err != nil {
		t.Fatal(err)
	}
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

// --- Browser → Go ---

func TestWebBridgeRoundTrip(t *testing.T) {
	bridge := transport.NewWebBridge()
	bridge.Register("hello.v1.HelloService/Greet", func(_ context.Context, payload json.RawMessage) (json.RawMessage, error) {
		var req struct {
			Name string `json:"name"`
		}
		json.Unmarshal(payload, &req)
		name := req.Name
		if name == "" {
			name = "World"
		}
		return json.Marshal(map[string]string{"message": fmt.Sprintf("Hello, %s!", name)})
	})

	srv := httptest.NewServer(bridge)
	defer srv.Close()

	c := dialTestBridge(t, srv)
	defer c.CloseNow()

	resp := roundTrip(t, c, `{"id":"1","method":"hello.v1.HelloService/Greet","payload":{"name":"Alice"}}`)
	if resp["id"] != "1" {
		t.Errorf("id = %v", resp["id"])
	}
	result := resp["result"].(map[string]interface{})
	if result["message"] != "Hello, Alice!" {
		t.Errorf("message = %v", result["message"])
	}
}

func TestWebBridgeDefaultName(t *testing.T) {
	bridge := transport.NewWebBridge()
	bridge.Register("hello.v1.HelloService/Greet", func(_ context.Context, payload json.RawMessage) (json.RawMessage, error) {
		var req struct{ Name string }
		json.Unmarshal(payload, &req)
		name := req.Name
		if name == "" {
			name = "World"
		}
		return json.Marshal(map[string]string{"message": fmt.Sprintf("Hello, %s!", name)})
	})

	srv := httptest.NewServer(bridge)
	defer srv.Close()

	c := dialTestBridge(t, srv)
	defer c.CloseNow()

	resp := roundTrip(t, c, `{"id":"2","method":"hello.v1.HelloService/Greet","payload":{}}`)
	result := resp["result"].(map[string]interface{})
	if result["message"] != "Hello, World!" {
		t.Errorf("message = %v", result["message"])
	}
}

func TestWebBridgeMethodNotFound(t *testing.T) {
	bridge := transport.NewWebBridge()
	srv := httptest.NewServer(bridge)
	defer srv.Close()

	c := dialTestBridge(t, srv)
	defer c.CloseNow()

	resp := roundTrip(t, c, `{"id":"3","method":"no.Such/Method"}`)
	errObj := resp["error"].(map[string]interface{})
	if errObj["code"].(float64) != 12 {
		t.Errorf("code = %v", errObj["code"])
	}
}

func TestWebBridgeMethods(t *testing.T) {
	bridge := transport.NewWebBridge()
	bridge.Register("a.B/C", func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) { return nil, nil })
	bridge.Register("d.E/F", func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) { return nil, nil })

	if len(bridge.Methods()) != 2 {
		t.Errorf("got %d methods, want 2", len(bridge.Methods()))
	}
}

// --- Go → Browser (bidirectional) ---

func TestWebBridgeGoCallsBrowser(t *testing.T) {
	bridge := transport.NewWebBridge()

	// Capture the WebConn when browser connects
	var conn *transport.WebConn
	var connReady sync.WaitGroup
	connReady.Add(1)
	bridge.OnConnect(func(c *transport.WebConn) {
		conn = c
		connReady.Done()
	})

	srv := httptest.NewServer(bridge)
	defer srv.Close()

	// Connect a "browser" that handles incoming requests
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := dialTestBridge(t, srv)
	defer c.CloseNow()

	// Browser-side handler: reads a request, sends a response
	go func() {
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			var msg map[string]interface{}
			json.Unmarshal(data, &msg)

			if method, ok := msg["method"].(string); ok && method == "ui.v1.UIService/GetViewport" {
				resp, _ := json.Marshal(map[string]interface{}{
					"id":     msg["id"],
					"result": map[string]int{"width": 1920, "height": 1080},
				})
				c.Write(ctx, websocket.MessageText, resp)
			}
		}
	}()

	connReady.Wait()

	// Go→Browser invocation
	payload, _ := json.Marshal(map[string]string{})
	result, err := conn.InvokeWithTimeout("ui.v1.UIService/GetViewport", payload, 2*time.Second)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}

	var viewport map[string]float64
	json.Unmarshal(result, &viewport)
	if viewport["width"] != 1920 {
		t.Errorf("width = %v", viewport["width"])
	}
	if viewport["height"] != 1080 {
		t.Errorf("height = %v", viewport["height"])
	}
}

func TestWebBridgeGoCallsBrowserError(t *testing.T) {
	bridge := transport.NewWebBridge()

	var conn *transport.WebConn
	var connReady sync.WaitGroup
	connReady.Add(1)
	bridge.OnConnect(func(c *transport.WebConn) {
		conn = c
		connReady.Done()
	})

	srv := httptest.NewServer(bridge)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := dialTestBridge(t, srv)
	defer c.CloseNow()

	// Browser always responds with an error
	go func() {
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			var msg map[string]interface{}
			json.Unmarshal(data, &msg)

			if _, ok := msg["method"]; ok {
				resp, _ := json.Marshal(map[string]interface{}{
					"id":    msg["id"],
					"error": map[string]interface{}{"code": 3, "message": "not supported"},
				})
				c.Write(ctx, websocket.MessageText, resp)
			}
		}
	}()

	connReady.Wait()

	_, err := conn.InvokeWithTimeout("any.Method/Here", nil, 2*time.Second)
	if err == nil {
		t.Fatal("expected error")
	}

	webErr, ok := err.(*transport.WebError)
	if !ok {
		t.Fatalf("expected WebError, got %T: %v", err, err)
	}
	if webErr.Code != 3 {
		t.Errorf("code = %d, want 3", webErr.Code)
	}
}
