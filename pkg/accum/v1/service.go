package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/organic-programming/go-holons/pkg/holonrpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// MethodRelay is the canonical Holon-RPC method name for Relay.
	MethodRelay = "accum.v1.Accum/Relay"
)

// ForwardGRPCFunc forwards a RelayRequest to a gRPC URI.
type ForwardGRPCFunc func(ctx context.Context, uri string, req *RelayRequest) (*RelayResponse, error)

// ForwardHolonRPCFunc forwards a RelayRequest to a Holon-RPC method.
type ForwardHolonRPCFunc func(ctx context.Context, method string, req *RelayRequest) (*RelayResponse, error)

// Service implements accum.v1.Accum.
type Service struct {
	UnimplementedAccumServer

	SDKName string

	ForwardGRPC     ForwardGRPCFunc
	ForwardHolonRPC ForwardHolonRPCFunc
}

// Relay increments counter, appends this SDK name to trace, and optionally forwards.
func (s *Service) Relay(ctx context.Context, in *RelayRequest) (*RelayResponse, error) {
	req := CloneRelayRequest(in)
	req.Counter++
	req.Trace = append(req.Trace, s.sdkName())

	next := strings.TrimSpace(req.Next)
	if next == "" {
		return &RelayResponse{
			Counter: req.Counter,
			Trace:   append([]string(nil), req.Trace...),
		}, nil
	}

	// Forward one hop. The downstream can decide its own next hop.
	req.Next = ""
	if isGRPCURI(next) {
		if s.ForwardGRPC == nil {
			return nil, status.Error(codes.Unavailable, "relay forwarding over gRPC is not configured")
		}
		return s.ForwardGRPC(ctx, next, req)
	}

	if s.ForwardHolonRPC == nil {
		return nil, status.Error(codes.Unavailable, "relay forwarding over holon-rpc is not configured")
	}
	return s.ForwardHolonRPC(ctx, next, req)
}

// HolonRPCHandler exposes Relay as a Holon-RPC handler.
func (s *Service) HolonRPCHandler(ctx context.Context, params map[string]any) (map[string]any, error) {
	req, err := RelayRequestFromMap(params)
	if err != nil {
		return nil, &holonrpc.ResponseError{
			Code:    -32602,
			Message: err.Error(),
		}
	}

	resp, err := s.Relay(ctx, req)
	if err != nil {
		return nil, toHolonRPCError(err)
	}
	return RelayResponseToMap(resp), nil
}

// CloneRelayRequest returns a deep copy. Nil input yields an empty request.
func CloneRelayRequest(in *RelayRequest) *RelayRequest {
	if in == nil {
		return &RelayRequest{}
	}
	return &RelayRequest{
		Counter: in.Counter,
		Trace:   append([]string(nil), in.Trace...),
		Next:    in.Next,
	}
}

// RelayRequestFromMap decodes Holon-RPC params to RelayRequest.
func RelayRequestFromMap(params map[string]any) (*RelayRequest, error) {
	if params == nil {
		return &RelayRequest{}, nil
	}

	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal relay params: %w", err)
	}

	var out RelayRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("decode relay params: %w", err)
	}
	return &out, nil
}

// RelayResponseFromMap decodes Holon-RPC result to RelayResponse.
func RelayResponseFromMap(result map[string]any) (*RelayResponse, error) {
	if result == nil {
		return &RelayResponse{}, nil
	}

	raw, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("marshal relay result: %w", err)
	}

	var out RelayResponse
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("decode relay result: %w", err)
	}
	return &out, nil
}

// RelayRequestToMap encodes RelayRequest for Holon-RPC invoke params.
func RelayRequestToMap(req *RelayRequest) map[string]any {
	if req == nil {
		return map[string]any{
			"counter": int32(0),
			"trace":   []string{},
			"next":    "",
		}
	}
	return map[string]any{
		"counter": req.Counter,
		"trace":   append([]string(nil), req.Trace...),
		"next":    req.Next,
	}
}

// RelayResponseToMap encodes RelayResponse for Holon-RPC result payload.
func RelayResponseToMap(resp *RelayResponse) map[string]any {
	if resp == nil {
		return map[string]any{
			"counter": int32(0),
			"trace":   []string{},
		}
	}
	return map[string]any{
		"counter": resp.Counter,
		"trace":   append([]string(nil), resp.Trace...),
	}
}

func (s *Service) sdkName() string {
	if trimmed := strings.TrimSpace(s.SDKName); trimmed != "" {
		return trimmed
	}
	return "go-holons"
}

func toHolonRPCError(err error) error {
	if err == nil {
		return nil
	}

	var rpcErr *holonrpc.ResponseError
	if errors.As(err, &rpcErr) {
		return rpcErr
	}

	if st, ok := status.FromError(err); ok {
		msg := st.Message()
		if msg == "" {
			msg = err.Error()
		}
		return &holonrpc.ResponseError{
			Code:    int(st.Code()),
			Message: msg,
		}
	}

	switch {
	case errors.Is(err, context.Canceled):
		return &holonrpc.ResponseError{Code: 1, Message: err.Error()}
	case errors.Is(err, context.DeadlineExceeded):
		return &holonrpc.ResponseError{Code: 4, Message: err.Error()}
	default:
		return &holonrpc.ResponseError{Code: 14, Message: err.Error()}
	}
}
