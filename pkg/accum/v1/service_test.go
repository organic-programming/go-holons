package v1

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/organic-programming/go-holons/pkg/holonrpc"
	"google.golang.org/grpc"
)

func TestRelay_SingleHop(t *testing.T) {
	svc := &Service{SDKName: "go-holons"}

	resp, err := svc.Relay(context.Background(), &RelayRequest{
		Counter: 0,
		Trace:   nil,
		Next:    "",
	})
	if err != nil {
		t.Fatalf("Relay returned error: %v", err)
	}
	if resp.Counter != 1 {
		t.Fatalf("counter = %d, want 1", resp.Counter)
	}
	if len(resp.Trace) != 1 || resp.Trace[0] != "go-holons" {
		t.Fatalf("trace = %#v, want [go-holons]", resp.Trace)
	}
}

func TestRelay_GRPCTwoHopChain(t *testing.T) {
	downstream := &Service{SDKName: "go-holons-downstream"}
	downstreamURI, stopDownstream := startAccumGRPCServer(t, downstream)
	defer stopDownstream()

	upstream := &Service{
		SDKName: "go-holons-upstream",
		ForwardGRPC: func(ctx context.Context, uri string, req *RelayRequest) (*RelayResponse, error) {
			forwardReq := CloneRelayRequest(req)
			forwardReq.Next = ""
			return InvokeRelayGRPC(ctx, uri, forwardReq, DialOptions{})
		},
	}
	upstreamURI, stopUpstream := startAccumGRPCServer(t, upstream)
	defer stopUpstream()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resp, err := InvokeRelayGRPC(ctx, upstreamURI, &RelayRequest{
		Counter: 0,
		Trace:   nil,
		Next:    downstreamURI,
	}, DialOptions{})
	if err != nil {
		t.Fatalf("InvokeRelayGRPC chain failed: %v", err)
	}

	if resp.Counter != 2 {
		t.Fatalf("counter = %d, want 2", resp.Counter)
	}
	if len(resp.Trace) != 2 {
		t.Fatalf("trace length = %d, want 2", len(resp.Trace))
	}
	if resp.Trace[0] != "go-holons-upstream" || resp.Trace[1] != "go-holons-downstream" {
		t.Fatalf("trace = %#v, want [go-holons-upstream go-holons-downstream]", resp.Trace)
	}
}

func TestRelay_HolonRPCRoundTrip(t *testing.T) {
	svc := &Service{SDKName: "go-holons"}

	server := holonrpc.NewServer("ws://127.0.0.1:0/rpc")
	server.Register(MethodRelay, svc.HolonRPCHandler)

	addr, err := server.Start()
	if err != nil {
		t.Fatalf("holon-rpc start failed: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resp, err := InvokeRelayHolonRPC(ctx, addr, MethodRelay, &RelayRequest{
		Counter: 0,
		Trace:   nil,
		Next:    "",
	}, "")
	if err != nil {
		t.Fatalf("InvokeRelayHolonRPC failed: %v", err)
	}

	if resp.Counter != 1 {
		t.Fatalf("counter = %d, want 1", resp.Counter)
	}
	if len(resp.Trace) != 1 || resp.Trace[0] != "go-holons" {
		t.Fatalf("trace = %#v, want [go-holons]", resp.Trace)
	}
}

func startAccumGRPCServer(t *testing.T, svc AccumServer) (string, func()) {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	server := grpc.NewServer()
	RegisterAccumServer(server, svc)

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(lis)
	}()

	stop := func() {
		server.GracefulStop()
		_ = lis.Close()
		select {
		case serveErr := <-errCh:
			if serveErr != nil &&
				!errors.Is(serveErr, grpc.ErrServerStopped) &&
				!strings.Contains(strings.ToLower(serveErr.Error()), "closed network connection") {
				t.Fatalf("server exited with error: %v", serveErr)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for gRPC server to stop")
		}
	}

	return "tcp://" + lis.Addr().String(), stop
}
