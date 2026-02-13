package holonrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"nhooyr.io/websocket"
)

// Client is a bidirectional Holon-RPC client.
//
// It can:
//   - invoke methods on a remote Holon-RPC server (client -> server)
//   - receive and handle server-initiated requests (server -> client)
type Client struct {
	stateMu sync.RWMutex
	ws      *websocket.Conn
	rxCtx   context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	closed  bool

	sendMu sync.Mutex

	handlersMu sync.RWMutex
	handlers   map[string]Handler

	pendingMu sync.Mutex
	pending   map[string]chan rpcMessage

	nextClientID int64
}

// NewClient creates an empty Holon-RPC client.
func NewClient() *Client {
	return &Client{
		handlers: make(map[string]Handler),
		pending:  make(map[string]chan rpcMessage),
	}
}

// Register registers a handler for server-initiated requests.
func (c *Client) Register(method string, handler Handler) {
	c.handlersMu.Lock()
	defer c.handlersMu.Unlock()
	c.handlers[method] = handler
}

// Unregister removes a previously registered handler.
func (c *Client) Unregister(method string) {
	c.handlersMu.Lock()
	defer c.handlersMu.Unlock()
	delete(c.handlers, method)
}

// Connect dials a Holon-RPC endpoint and starts the receive loop.
func (c *Client) Connect(ctx context.Context, url string) error {
	if strings.TrimSpace(url) == "" {
		return errors.New("holon-rpc: url is required")
	}

	c.stateMu.Lock()
	if c.closed {
		c.stateMu.Unlock()
		return errors.New("holon-rpc: client is closed")
	}
	if c.ws != nil {
		c.stateMu.Unlock()
		return errors.New("holon-rpc: client already connected")
	}
	c.stateMu.Unlock()

	ws, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		Subprotocols: []string{"holon-rpc"},
	})
	if err != nil {
		return fmt.Errorf("holon-rpc: dial failed: %w", err)
	}

	if ws.Subprotocol() != "holon-rpc" {
		_ = ws.Close(websocket.StatusProtocolError, "missing holon-rpc subprotocol")
		return errors.New("holon-rpc: server did not negotiate holon-rpc")
	}

	rxCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	c.stateMu.Lock()
	if c.closed {
		c.stateMu.Unlock()
		cancel()
		_ = ws.Close(websocket.StatusNormalClosure, "client closed")
		return errors.New("holon-rpc: client is closed")
	}
	c.ws = ws
	c.rxCtx = rxCtx
	c.cancel = cancel
	c.done = done
	c.stateMu.Unlock()

	go c.readLoop(ws, done)
	return nil
}

// Close gracefully closes the client connection and stops runtime goroutines.
func (c *Client) Close() error {
	c.stateMu.Lock()
	if c.closed {
		c.stateMu.Unlock()
		return nil
	}
	c.closed = true
	ws := c.ws
	cancel := c.cancel
	done := c.done
	c.ws = nil
	c.cancel = nil
	c.done = nil
	c.stateMu.Unlock()

	if cancel != nil {
		cancel()
	}

	if ws != nil {
		_ = ws.Close(websocket.StatusNormalClosure, "client close")
	}
	if done != nil {
		<-done
	}

	c.failAllPending(errConnectionClosed)
	return nil
}

// Invoke sends a JSON-RPC request and waits for the corresponding response.
func (c *Client) Invoke(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(method) == "" {
		return nil, errors.New("holon-rpc: method is required")
	}

	ws, rxCtx, err := c.currentConn()
	if err != nil {
		return nil, err
	}

	id := fmt.Sprintf("c%d", atomic.AddInt64(&c.nextClientID, 1))
	idRaw := makeID(id)
	key, _ := idKey(idRaw)

	ch := make(chan rpcMessage, 1)
	c.pendingMu.Lock()
	c.pending[key] = ch
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, key)
		c.pendingMu.Unlock()
	}()

	paramsRaw, err := marshalObject(params)
	if err != nil {
		return nil, fmt.Errorf("holon-rpc: marshal params: %w", err)
	}

	msg, err := marshalMessage(rpcMessage{
		JSONRPC: jsonRPCVersion,
		ID:      idRaw,
		Method:  method,
		Params:  paramsRaw,
	})
	if err != nil {
		return nil, fmt.Errorf("holon-rpc: marshal request: %w", err)
	}

	if err := c.write(ws, ctx, msg); err != nil {
		return nil, err
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		out, err := decodeResult(resp.Result)
		if err != nil {
			return nil, fmt.Errorf("holon-rpc: invalid result: %w", err)
		}
		return out, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-rxCtx.Done():
		return nil, errConnectionClosed
	}
}

func (c *Client) currentConn() (*websocket.Conn, context.Context, error) {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()

	if c.closed {
		return nil, nil, errors.New("holon-rpc: client is closed")
	}
	if c.ws == nil || c.cancel == nil || c.rxCtx == nil {
		return nil, nil, errors.New("holon-rpc: client is not connected")
	}
	return c.ws, c.rxCtx, nil
}

func (c *Client) readLoop(ws *websocket.Conn, done chan struct{}) {
	defer close(done)
	defer c.onDisconnect(ws)

	for {
		kind, data, err := ws.Read(c.rxCtx)
		if err != nil {
			return
		}
		if kind != websocket.MessageText {
			continue
		}

		var msg rpcMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			_ = c.sendError(ws, c.rxCtx, json.RawMessage("null"), codeParseError, "parse error", nil)
			continue
		}

		if msg.Method != "" {
			go c.handleRequest(ws, c.rxCtx, msg)
			continue
		}

		if len(msg.Result) > 0 || msg.Error != nil {
			c.handleResponse(msg)
			continue
		}

		if hasID(msg.ID) {
			_ = c.sendError(ws, c.rxCtx, msg.ID, codeInvalidRequest, "invalid request", nil)
		}
	}
}

func (c *Client) onDisconnect(ws *websocket.Conn) {
	c.stateMu.Lock()
	if c.ws == ws {
		c.ws = nil
		if c.cancel != nil {
			c.cancel()
		}
		c.cancel = nil
		c.rxCtx = nil
		c.done = nil
	}
	c.stateMu.Unlock()

	c.failAllPending(errConnectionClosed)
}

func (c *Client) handleRequest(ws *websocket.Conn, ctx context.Context, msg rpcMessage) {
	reqID := msg.ID

	if msg.JSONRPC != jsonRPCVersion {
		if hasID(reqID) {
			_ = c.sendError(ws, ctx, reqID, codeInvalidRequest, "invalid request", nil)
		}
		return
	}

	method := strings.TrimSpace(msg.Method)
	if method == "" {
		if hasID(reqID) {
			_ = c.sendError(ws, ctx, reqID, codeInvalidRequest, "invalid request", nil)
		}
		return
	}

	if method == "rpc.heartbeat" {
		if hasID(reqID) {
			_ = c.sendResult(ws, ctx, reqID, map[string]any{})
		}
		return
	}

	if hasID(reqID) {
		sid, err := decodeStringID(reqID)
		if err != nil || !strings.HasPrefix(sid, "s") {
			_ = c.sendError(ws, ctx, reqID, codeInvalidRequest, "server request id must start with 's'", nil)
			return
		}
	}

	params, err := decodeParams(msg.Params)
	if err != nil {
		if hasID(reqID) {
			_ = c.sendError(ws, ctx, reqID, codeInvalidParams, err.Error(), nil)
		}
		return
	}

	c.handlersMu.RLock()
	handler, ok := c.handlers[method]
	c.handlersMu.RUnlock()
	if !ok {
		if hasID(reqID) {
			_ = c.sendError(ws, ctx, reqID, codeMethodNotFound, fmt.Sprintf("method %q not found", method), nil)
		}
		return
	}

	result, err := handler(ctx, params)
	if err != nil {
		if !hasID(reqID) {
			return
		}
		var rpcErr *ResponseError
		if errors.As(err, &rpcErr) {
			_ = c.sendError(ws, ctx, reqID, rpcErr.Code, rpcErr.Message, rpcErr.Data)
			return
		}
		_ = c.sendError(ws, ctx, reqID, 13, err.Error(), nil)
		return
	}

	if hasID(reqID) {
		_ = c.sendResult(ws, ctx, reqID, result)
	}
}

func (c *Client) handleResponse(msg rpcMessage) {
	key, ok := idKey(msg.ID)
	if !ok {
		return
	}

	c.pendingMu.Lock()
	ch, exists := c.pending[key]
	c.pendingMu.Unlock()
	if !exists {
		return
	}

	if msg.JSONRPC != jsonRPCVersion {
		msg.Error = &ResponseError{
			Code:    codeInvalidRequest,
			Message: "invalid response",
		}
		msg.Result = nil
	}

	select {
	case ch <- msg:
	default:
	}
}

func (c *Client) sendResult(ws *websocket.Conn, ctx context.Context, id json.RawMessage, result map[string]any) error {
	resultRaw, err := marshalObject(result)
	if err != nil {
		return err
	}

	data, err := marshalMessage(rpcMessage{
		JSONRPC: jsonRPCVersion,
		ID:      id,
		Result:  resultRaw,
	})
	if err != nil {
		return err
	}
	return c.write(ws, ctx, data)
}

func (c *Client) sendError(ws *websocket.Conn, ctx context.Context, id json.RawMessage, code int, message string, data any) error {
	errBody := &ResponseError{
		Code:    code,
		Message: message,
		Data:    data,
	}
	dataJSON, err := marshalMessage(rpcMessage{
		JSONRPC: jsonRPCVersion,
		ID:      id,
		Error:   errBody,
	})
	if err != nil {
		return err
	}
	return c.write(ws, ctx, dataJSON)
}

func (c *Client) write(ws *websocket.Conn, ctx context.Context, payload []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	if err := ws.Write(ctx, websocket.MessageText, payload); err != nil {
		return fmt.Errorf("holon-rpc: write failed: %w", err)
	}
	return nil
}

func (c *Client) failAllPending(err error) {
	c.pendingMu.Lock()
	pending := c.pending
	c.pending = make(map[string]chan rpcMessage)
	c.pendingMu.Unlock()

	for _, ch := range pending {
		select {
		case ch <- rpcMessage{
			JSONRPC: jsonRPCVersion,
			Error: &ResponseError{
				Code:    codeUnavailable,
				Message: err.Error(),
			},
		}:
		default:
		}
	}
}
