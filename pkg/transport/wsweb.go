package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"

	"nhooyr.io/websocket"
)

// WebBridge provides a JSON-over-WebSocket gateway for browser clients.
// It exposes holon methods via a simple JSON-RPC-style protocol without
// introducing any third-party wire format.
//
// Wire protocol (browser → server):
//
//	{ "id": "1", "method": "hello.v1.HelloService/Greet", "payload": {"name":"Alice"} }
//
// Wire protocol (server → browser):
//
//	{ "id": "1", "result": {"message":"Hello, Alice!"} }
//	{ "id": "1", "error": {"code": 5, "message": "not found"} }
//
// Handlers are registered via Register(). Each handler receives raw JSON
// and returns raw JSON — the bridge itself is codec-agnostic.
type WebBridge struct {
	mu       sync.RWMutex
	handlers map[string]WebHandler // "package.Service/Method" → handler
	origins  map[string]bool       // allowed CORS origins, nil = allow all
}

// WebHandler processes a single RPC call. It receives the JSON payload
// from the browser and returns the JSON result or an error.
type WebHandler func(ctx context.Context, payload json.RawMessage) (json.RawMessage, error)

// WebError is returned by handlers to communicate a specific error code.
type WebError struct {
	Code    int
	Message string
}

func (e *WebError) Error() string {
	return fmt.Sprintf("code %d: %s", e.Code, e.Message)
}

// NewWebBridge creates an empty bridge. Register handlers before mounting.
func NewWebBridge() *WebBridge {
	return &WebBridge{
		handlers: make(map[string]WebHandler),
	}
}

// Register adds a method handler. The method string must follow the
// "package.Service/Method" convention (matching the gRPC full method path).
func (b *WebBridge) Register(method string, handler WebHandler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[method] = handler
}

// AllowOrigins restricts CORS to the given origins.
// Pass nothing to allow all origins (development mode).
func (b *WebBridge) AllowOrigins(origins ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(origins) == 0 {
		b.origins = nil
		return
	}
	b.origins = make(map[string]bool, len(origins))
	for _, o := range origins {
		b.origins[o] = true
	}
}

// Methods returns all registered method names.
func (b *WebBridge) Methods() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	methods := make([]string, 0, len(b.handlers))
	for m := range b.handlers {
		methods = append(methods, m)
	}
	return methods
}

// ServeHTTP implements http.Handler, delegating to HandleWebSocket.
// This lets the bridge be used directly with http.ListenAndServe or
// httptest.NewServer without wrapping.
func (b *WebBridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.HandleWebSocket(w, r)
}

// HandleWebSocket is the HTTP handler for the browser endpoint.
// Mount it on the HTTP mux at a path like "/ws".
func (b *WebBridge) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	opts := &websocket.AcceptOptions{
		Subprotocols: []string{"holon-web"},
	}

	b.mu.RLock()
	if b.origins == nil {
		opts.InsecureSkipVerify = true
	} else {
		origin := r.Header.Get("Origin")
		if !b.origins[origin] {
			b.mu.RUnlock()
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		opts.OriginPatterns = make([]string, 0, len(b.origins))
		for o := range b.origins {
			opts.OriginPatterns = append(opts.OriginPatterns, o)
		}
	}
	b.mu.RUnlock()

	c, err := websocket.Accept(w, r, opts)
	if err != nil {
		http.Error(w, "websocket upgrade failed", http.StatusBadRequest)
		return
	}
	defer c.CloseNow()

	ctx := r.Context()
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return // client disconnected
		}

		resp := b.handleMessage(ctx, data)
		if err := c.Write(ctx, websocket.MessageText, resp); err != nil {
			return
		}
	}
}

// --- wire types ---

type wsReq struct {
	ID      string          `json:"id"`
	Method  string          `json:"method"`  // "package.Service/Method"
	Payload json.RawMessage `json:"payload"` // JSON input
}

type wsResp struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *wsErr          `json:"error,omitempty"`
}

type wsErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (b *WebBridge) handleMessage(ctx context.Context, data []byte) []byte {
	var req wsReq
	if err := json.Unmarshal(data, &req); err != nil {
		return marshalWsResp(wsResp{Error: &wsErr{Code: 3, Message: "invalid JSON"}})
	}
	if req.Method == "" {
		return marshalWsResp(wsResp{ID: req.ID, Error: &wsErr{Code: 3, Message: "method is required"}})
	}
	if !strings.Contains(req.Method, "/") {
		return marshalWsResp(wsResp{ID: req.ID, Error: &wsErr{
			Code: 3, Message: fmt.Sprintf("method must be Service/Method, got %q", req.Method),
		}})
	}

	b.mu.RLock()
	handler, ok := b.handlers[req.Method]
	b.mu.RUnlock()

	if !ok {
		return marshalWsResp(wsResp{ID: req.ID, Error: &wsErr{
			Code: 12, Message: fmt.Sprintf("method %q not registered", req.Method),
		}})
	}

	payload := req.Payload
	if payload == nil {
		payload = json.RawMessage("{}")
	}

	result, err := handler(ctx, payload)
	if err != nil {
		code := 13 // INTERNAL
		msg := err.Error()
		if we, ok := err.(*WebError); ok {
			code = we.Code
			msg = we.Message
		}
		return marshalWsResp(wsResp{ID: req.ID, Error: &wsErr{Code: code, Message: msg}})
	}

	return marshalWsResp(wsResp{ID: req.ID, Result: result})
}

func marshalWsResp(r wsResp) []byte {
	data, err := json.Marshal(r)
	if err != nil {
		log.Printf("wsweb: marshal response: %v", err)
		return []byte(`{"error":{"code":13,"message":"internal marshal error"}}`)
	}
	return data
}
