package transport_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
		Subprotocols: []string{"holon-rpc"},
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
	if got := resp["jsonrpc"]; got != "2.0" {
		t.Fatalf("jsonrpc = %v, want 2.0", got)
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

	resp := roundTrip(t, c, `{"jsonrpc":"2.0","id":"1","method":"hello.v1.HelloService/Greet","params":{"name":"Alice"}}`)
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

	resp := roundTrip(t, c, `{"jsonrpc":"2.0","id":"2","method":"hello.v1.HelloService/Greet","params":{}}`)
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

	resp := roundTrip(t, c, `{"jsonrpc":"2.0","id":"3","method":"no.Such/Method"}`)
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
					"jsonrpc": "2.0",
					"id":      msg["id"],
					"result":  map[string]int{"width": 1920, "height": 1080},
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
					"jsonrpc": "2.0",
					"id":      msg["id"],
					"error":   map[string]interface{}{"code": 3, "message": "not supported"},
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
	if got := resp["jsonrpc"]; got != "2.0" {
		t.Fatalf("jsonrpc = %v, want 2.0", got)
	}

	errObj, ok := resp["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected error object, got: %v", resp)
	}
	if errObj["code"].(float64) != -32700 {
		t.Fatalf("expected code=-32700 for invalid JSON, got: %v", errObj["code"])
	}
}

func TestWebBridgeHeartbeatCompatibility(t *testing.T) {
	bridge := transport.NewWebBridge()
	srv := httptest.NewServer(bridge)
	defer srv.Close()

	c := dialTestBridge(t, srv)
	defer c.CloseNow()

	resp := roundTrip(t, c, `{"jsonrpc":"2.0","id":"h1","method":"rpc.heartbeat","params":{}}`)
	if resp["id"] != "h1" {
		t.Fatalf("id = %v, want h1", resp["id"])
	}
	result, ok := resp["result"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected success envelope for rpc.heartbeat: %v", resp)
	}
	if len(result) != 0 {
		t.Fatalf("heartbeat result = %v, want {}", result)
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
				"jsonrpc": "2.0",
				"id":      "unknown-" + id,
				"result":  map[string]bool{"ignored": true},
			})
			c.Write(ctx, websocket.MessageText, unknownResp)

			mainResp, _ := json.Marshal(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  map[string]string{"echoId": id},
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

func TestWebBridgeConcurrentInvokeMultipleClients(t *testing.T) {
	bridge := transport.NewWebBridge()

	const clientCount = 4
	connCh := make(chan *transport.WebConn, clientCount)
	bridge.OnConnect(func(c *transport.WebConn) {
		connCh <- c
	})

	srv := httptest.NewServer(bridge)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	browserConns := make([]*websocket.Conn, 0, clientCount)
	for i := 0; i < clientCount; i++ {
		c := dialTestBridge(t, srv)
		browserConns = append(browserConns, c)

		clientID := i
		go func(conn *websocket.Conn, id int) {
			for {
				_, data, err := conn.Read(ctx)
				if err != nil {
					return
				}

				var req map[string]interface{}
				if err := json.Unmarshal(data, &req); err != nil {
					continue
				}

				reqID, _ := req["id"].(string)
				if reqID == "" {
					continue
				}
				if _, ok := req["method"].(string); !ok {
					continue
				}

				resp, _ := json.Marshal(map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      reqID,
					"result":  map[string]int{"client": id},
				})
				_ = conn.Write(ctx, websocket.MessageText, resp)
			}
		}(c, clientID)
	}
	defer func() {
		for _, c := range browserConns {
			c.CloseNow()
		}
	}()

	webConns := make([]*transport.WebConn, 0, clientCount)
	for i := 0; i < clientCount; i++ {
		select {
		case wc := <-connCh:
			webConns = append(webConns, wc)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for connection %d", i+1)
		}
	}

	type invokeResult struct {
		client int
		got    int
		err    error
	}
	results := make(chan invokeResult, clientCount)

	var wg sync.WaitGroup
	for i, wc := range webConns {
		wg.Add(1)
		go func(client int, conn *transport.WebConn) {
			defer wg.Done()

			payload, _ := json.Marshal(map[string]int{"requestClient": client})
			out, err := conn.InvokeWithTimeout("ui.v1.UIService/GetViewport", payload, 2*time.Second)
			if err != nil {
				results <- invokeResult{client: client, err: err}
				return
			}

			var body map[string]int
			if err := json.Unmarshal(out, &body); err != nil {
				results <- invokeResult{client: client, err: err}
				return
			}
			results <- invokeResult{client: client, got: body["client"]}
		}(i, wc)
	}
	wg.Wait()
	close(results)

	seen := make(map[int]bool, clientCount)
	for r := range results {
		if r.err != nil {
			t.Fatalf("invoke for client %d failed: %v", r.client, r.err)
		}
		seen[r.got] = true
	}

	if len(seen) != clientCount {
		t.Fatalf("expected responses from %d distinct clients, got %d", clientCount, len(seen))
	}
}

func TestWebBridgeInvokeConnectionDropMidCall(t *testing.T) {
	bridge := transport.NewWebBridge()

	connCh := make(chan *transport.WebConn, 1)
	bridge.OnConnect(func(c *transport.WebConn) {
		connCh <- c
	})

	srv := httptest.NewServer(bridge)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := dialTestBridge(t, srv)
	defer c.CloseNow()

	go func() {
		_, _, err := c.Read(ctx)
		if err != nil {
			return
		}
		_ = c.Close(websocket.StatusNormalClosure, "disconnect mid-invoke")
	}()

	var conn *transport.WebConn
	select {
	case conn = <-connCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for server-side connection")
	}

	_, err := conn.InvokeWithTimeout("ui.v1.UIService/GetViewport", nil, 2*time.Second)
	if err == nil {
		t.Fatal("expected invoke to fail after client disconnect")
	}
	if !strings.Contains(err.Error(), "connection closed") && !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("unexpected error on disconnect: %v", err)
	}
}

func TestWebBridgeMalformedJSONPayloads(t *testing.T) {
	bridge := transport.NewWebBridge()
	bridge.Register("hello.v1.HelloService/Greet", func(_ context.Context, payload json.RawMessage) (json.RawMessage, error) {
		var req struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, &transport.WebError{Code: 3, Message: "invalid payload"}
		}
		return json.Marshal(map[string]string{"message": fmt.Sprintf("Hello, %s!", req.Name)})
	})

	srv := httptest.NewServer(bridge)
	defer srv.Close()

	c := dialTestBridge(t, srv)
	defer c.CloseNow()

	testCases := []struct {
		name     string
		message  string
		wantCode float64
	}{
		{
			name:     "invalid-json-document",
			message:  `{bad-json`,
			wantCode: -32700,
		},
		{
			name:     "invalid-envelope-field-type",
			message:  `{"jsonrpc":"2.0","id":"e1","method":123,"params":{}}`,
			wantCode: -32700,
		},
		{
			name:     "malformed-handler-payload-shape",
			message:  `{"jsonrpc":"2.0","id":"e2","method":"hello.v1.HelloService/Greet","params":"not-an-object"}`,
			wantCode: 3,
		},
	}

	ctx := context.Background()
	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if err := c.Write(ctx, websocket.MessageText, []byte(tc.message)); err != nil {
				t.Fatalf("write malformed payload: %v", err)
			}

			_, data, err := c.Read(ctx)
			if err != nil {
				t.Fatalf("read malformed payload response: %v", err)
			}

			var resp map[string]interface{}
			if err := json.Unmarshal(data, &resp); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			if got := resp["jsonrpc"]; got != "2.0" {
				t.Fatalf("jsonrpc = %v, want 2.0", got)
			}

			errObj, ok := resp["error"].(map[string]interface{})
			if !ok {
				t.Fatalf("expected error response, got: %v", resp)
			}
			if errObj["code"].(float64) != tc.wantCode {
				t.Fatalf("error code = %v, want %v", errObj["code"], tc.wantCode)
			}
		})
	}
}

func TestWebBridgeAllowOrigins(t *testing.T) {
	bridge := transport.NewWebBridge()
	bridge.AllowOrigins("allowed.example")

	srv := httptest.NewServer(bridge)
	defer srv.Close()

	ctx := context.Background()
	wsURL := "ws" + srv.URL[4:]

	_, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		Subprotocols: []string{"holon-rpc"},
		HTTPHeader:   http.Header{"Origin": []string{"blocked.example"}},
	})
	if err == nil {
		t.Fatal("expected origin check failure")
	}

	bridge.AllowOrigins()
	openConn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		Subprotocols: []string{"holon-rpc"},
	})
	if err != nil {
		t.Fatalf("dial with unrestricted origins: %v", err)
	}
	openConn.CloseNow()
}

func TestWebBridgeMarshalResponseFailure(t *testing.T) {
	bridge := transport.NewWebBridge()
	bridge.Register("bad.v1.Service/Method", func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
		// Invalid RawMessage bytes trigger marshalWsResp fallback envelope.
		return json.RawMessage(`{bad-json`), nil
	})

	srv := httptest.NewServer(bridge)
	defer srv.Close()

	c := dialTestBridge(t, srv)
	defer c.CloseNow()

	resp := roundTrip(t, c, `{"jsonrpc":"2.0","id":"m1","method":"bad.v1.Service/Method","params":{}}`)
	errObj, ok := resp["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected error response, got: %v", resp)
	}
	if errObj["code"].(float64) != 13 {
		t.Fatalf("error code = %v, want 13", errObj["code"])
	}
}
