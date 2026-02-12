package transport_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"nhooyr.io/websocket"

	"github.com/Organic-Programming/go-holons/pkg/transport"
)

func TestWebBridgeRoundTrip(t *testing.T) {
	bridge := transport.NewWebBridge()

	// Register a simple greet handler
	bridge.Register("hello.v1.HelloService/Greet", func(_ context.Context, payload json.RawMessage) (json.RawMessage, error) {
		var req struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
		name := req.Name
		if name == "" {
			name = "World"
		}
		return json.Marshal(map[string]string{"message": fmt.Sprintf("Hello, %s!", name)})
	})

	// Start a test HTTP server with the bridge handler
	srv := httptest.NewServer(bridge)
	defer srv.Close()

	// Dial via WebSocket
	wsURL := "ws" + srv.URL[4:] // http → ws
	ctx := context.Background()
	c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		Subprotocols: []string{"holon-web"},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()

	// Send a request
	reqJSON := `{"id":"1","method":"hello.v1.HelloService/Greet","payload":{"name":"Alice"}}`
	if err := c.Write(ctx, websocket.MessageText, []byte(reqJSON)); err != nil {
		t.Fatal(err)
	}

	// Read response
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var resp struct {
		ID     string          `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatal(err)
	}

	if resp.ID != "1" {
		t.Errorf("id = %q, want 1", resp.ID)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	var result map[string]string
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result["message"] != "Hello, Alice!" {
		t.Errorf("message = %q, want %q", result["message"], "Hello, Alice!")
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

	ctx := context.Background()
	c, _, err := websocket.Dial(ctx, "ws"+srv.URL[4:], &websocket.DialOptions{
		Subprotocols: []string{"holon-web"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()

	// Empty payload → default "World"
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"id":"2","method":"hello.v1.HelloService/Greet","payload":{}}`)); err != nil {
		t.Fatal(err)
	}

	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var resp struct{ Result json.RawMessage }
	json.Unmarshal(data, &resp)
	var result map[string]string
	json.Unmarshal(resp.Result, &result)

	if result["message"] != "Hello, World!" {
		t.Errorf("message = %q, want %q", result["message"], "Hello, World!")
	}
}

func TestWebBridgeMethodNotFound(t *testing.T) {
	bridge := transport.NewWebBridge()

	srv := httptest.NewServer(bridge)
	defer srv.Close()

	ctx := context.Background()
	c, _, err := websocket.Dial(ctx, "ws"+srv.URL[4:], &websocket.DialOptions{
		Subprotocols: []string{"holon-web"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()

	if err := c.Write(ctx, websocket.MessageText, []byte(`{"id":"3","method":"no.Such/Method"}`)); err != nil {
		t.Fatal(err)
	}

	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var resp struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal(data, &resp)

	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != 12 {
		t.Errorf("code = %d, want 12 (UNIMPLEMENTED)", resp.Error.Code)
	}
}

func TestWebBridgeMethods(t *testing.T) {
	bridge := transport.NewWebBridge()
	bridge.Register("a.B/C", func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
		return nil, nil
	})
	bridge.Register("d.E/F", func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
		return nil, nil
	})

	methods := bridge.Methods()
	if len(methods) != 2 {
		t.Errorf("got %d methods, want 2", len(methods))
	}
}
