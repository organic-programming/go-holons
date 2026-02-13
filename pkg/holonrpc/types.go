// Package holonrpc implements Holon-RPC (JSON-RPC 2.0 over WebSocket).
//
// It follows PROTOCOL.md §4:
//   - WebSocket subprotocol: "holon-rpc"
//   - JSON envelope: {"jsonrpc":"2.0", ...}
//   - Bidirectional requests (both sides can initiate calls)
//   - Server-originated IDs must start with "s"
package holonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

const jsonRPCVersion = "2.0"

// JSON-RPC reserved error codes.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// Common gRPC-style transport/runtime code used by the SDK for disconnected
// peers (maps to UNAVAILABLE in PROTOCOL.md §5).
const codeUnavailable = 14

var errConnectionClosed = errors.New("holon-rpc connection closed")

// Handler handles one incoming Holon-RPC request.
//
// The params object is always a JSON object (never an array). Return a JSON
// object for the "result" field. Returning nil is treated as an empty object.
type Handler func(ctx context.Context, params map[string]any) (map[string]any, error)

// ResponseError represents a JSON-RPC error response.
type ResponseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *ResponseError  `json:"error,omitempty"`
}

func makeID(id string) json.RawMessage {
	b, _ := json.Marshal(id)
	return json.RawMessage(b)
}

func hasID(id json.RawMessage) bool {
	trimmed := bytes.TrimSpace(id)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func idKey(id json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(id)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", false
	}
	return string(trimmed), true
}

func decodeStringID(id json.RawMessage) (string, error) {
	var out string
	if err := json.Unmarshal(bytes.TrimSpace(id), &out); err != nil {
		return "", errors.New("id must be a string")
	}
	return out, nil
}

func decodeParams(raw json.RawMessage) (map[string]any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return map[string]any{}, nil
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return nil, errors.New("params must be an object")
	}

	var params map[string]any
	if err := json.Unmarshal(trimmed, &params); err != nil {
		return nil, errors.New("params must be an object")
	}
	if params == nil {
		return map[string]any{}, nil
	}
	return params, nil
}

func decodeResult(raw json.RawMessage) (map[string]any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return map[string]any{}, nil
	}

	var value any
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return nil, err
	}

	if value == nil {
		return map[string]any{}, nil
	}
	if obj, ok := value.(map[string]any); ok {
		return obj, nil
	}
	return map[string]any{"value": value}, nil
}

func marshalObject(obj map[string]any) (json.RawMessage, error) {
	if obj == nil {
		return json.RawMessage("{}"), nil
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

func marshalMessage(msg rpcMessage) ([]byte, error) {
	out, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return out, nil
}
