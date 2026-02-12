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

func TestWebBridgeInvalidJSON(t *testing.T) {
	bridge := transport.NewWebBridge()
	srv := httptest.NewServer(bridge)
	defer srv.Close()

	c := dialTestBridge(t, srv)
	defer c.CloseNow()

	ctx := context.Background()
	if err := c.Write(ctx, websocket.MessageText, []byte(`{bad-json`)); err != nil {
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

	errObj, ok := resp["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected error object, got: %v", resp)
	}
	if errObj["code"].(float64) != 3 {
		t.Fatalf("expected code=3 for invalid JSON, got: %v", errObj["code"])
	}
}

func TestWebBridgeHeartbeatCompatibility(t *testing.T) {
	bridge := transport.NewWebBridge()
	srv := httptest.NewServer(bridge)
	defer srv.Close()

	c := dialTestBridge(t, srv)
	defer c.CloseNow()

	resp := roundTrip(t, c, `{"id":"h1","method":"holon-web/Heartbeat","payload":{}}`)
	if resp["id"] != "h1" {
		t.Fatalf("id = %v, want h1", resp["id"])
	}
	errObj, ok := resp["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected error envelope for unknown heartbeat method: %v", resp)
	}
	if errObj["code"].(float64) != 12 {
		t.Fatalf("code = %v, want 12", errObj["code"])
	}
}

func TestWebBridgeIgnoresUnknownAndDuplicateResponseIDs(t *testing.T) {
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

	// Browser behavior:
	// 1) send an unknown response id (must be ignored by bridge)
	// 2) send the expected response
	// 3) send a duplicate response with the same id (must be ignored)
	go func() {
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}

			var req map[string]interface{}
			if err := json.Unmarshal(data, &req); err != nil {
				continue
			}

			id, ok := req["id"].(string)
			if !ok || id == "" {
				continue
			}
			if _, ok := req["method"].(string); !ok {
				continue
			}

			unknownResp, _ := json.Marshal(map[string]interface{}{
				"id":     "unknown-" + id,
				"result": map[string]bool{"ignored": true},
			})
			c.Write(ctx, websocket.MessageText, unknownResp)

			mainResp, _ := json.Marshal(map[string]interface{}{
				"id":     id,
				"result": map[string]string{"echoId": id},
			})
			c.Write(ctx, websocket.MessageText, mainResp)
			c.Write(ctx, websocket.MessageText, mainResp)
		}
	}()

	connReady.Wait()

	for i := 0; i < 2; i++ {
		payload, _ := json.Marshal(map[string]string{})
		result, err := conn.InvokeWithTimeout("ui.v1.UIService/GetViewport", payload, 2*time.Second)
		if err != nil {
			t.Fatalf("invoke %d failed: %v", i+1, err)
		}

		var out map[string]string
		if err := json.Unmarshal(result, &out); err != nil {
			t.Fatalf("unmarshal invoke %d result: %v", i+1, err)
		}
		if out["echoId"] == "" {
			t.Fatalf("invoke %d returned empty echoId: %v", i+1, out)
		}
	}
}
