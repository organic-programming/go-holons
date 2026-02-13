package grpcclient_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Organic-Programming/go-holons/pkg/grpcclient"
	"github.com/Organic-Programming/go-holons/pkg/serve"
	"github.com/Organic-Programming/go-holons/pkg/transport"

	"google.golang.org/grpc"
	testgrpc "google.golang.org/grpc/interop/grpc_testing"
)

type echoTestServer struct {
	testgrpc.UnimplementedTestServiceServer
}

func (s *echoTestServer) EmptyCall(context.Context, *testgrpc.Empty) (*testgrpc.Empty, error) {
	return &testgrpc.Empty{}, nil
}

func (s *echoTestServer) UnaryCall(_ context.Context, in *testgrpc.SimpleRequest) (*testgrpc.SimpleResponse, error) {
	payload := in.GetPayload()
	if payload == nil {
		payload = &testgrpc.Payload{Type: testgrpc.PayloadType_COMPRESSABLE, Body: []byte("echo")}
	}
	return &testgrpc.SimpleResponse{
		Payload: &testgrpc.Payload{
			Type: payload.GetType(),
			Body: append([]byte(nil), payload.GetBody()...),
		},
	}, nil
}

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		listenURI := serve.ParseFlags(os.Args[2:])
		err := serve.RunWithOptions(listenURI, func(s *grpc.Server) {
			testgrpc.RegisterTestServiceServer(s, &echoTestServer{})
		}, false)
		if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	os.Exit(m.Run())
}

func TestDialTCPRoundTrip(t *testing.T) {
	lis, err := transport.Listen("tcp://127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	startEchoGRPCServer(t, lis)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpcclient.Dial(ctx, lis.Addr().String())
	if err != nil {
		t.Fatalf("dial tcp: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	requireUnaryEchoEventually(t, conn, "tcp-echo")
}

func TestDialUnixRoundTrip(t *testing.T) {
	sockPath := t.TempDir() + "/grpc.sock"
	lis, err := transport.Listen("unix://" + sockPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	startEchoGRPCServer(t, lis)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpcclient.Dial(ctx, "unix://"+sockPath)
	if err != nil {
		t.Fatalf("dial unix: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	requireUnaryEchoEventually(t, conn, "unix-echo")
}

func TestDialMemRoundTrip(t *testing.T) {
	mem := transport.NewMemListener()
	startEchoGRPCServer(t, mem)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpcclient.DialMem(ctx, mem)
	if err != nil {
		t.Fatalf("dial mem: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	requireUnaryEchoEventually(t, conn, "mem-echo")
}

func TestDialWebSocketRoundTrip(t *testing.T) {
	lis, err := transport.Listen("ws://127.0.0.1:0/grpc")
	if err != nil {
		t.Fatalf("listen ws: %v", err)
	}
	startEchoGRPCServer(t, lis)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var conn *grpc.ClientConn
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err = grpcclient.DialWebSocket(ctx, lis.Addr().String())
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial ws failed after retries: %v", err)
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Cleanup(func() { _ = conn.Close() })

	requireUnaryEchoEventually(t, conn, "ws-echo")
}

func TestDialErrorCases(t *testing.T) {
	testCases := []struct {
		name    string
		address string
	}{
		{name: "invalid-unix-uri", address: "unix://"},
		{name: "unreachable-host", address: "127.0.0.1:1"},
		{name: "bad-scheme", address: "bad://host:12345"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			conn, err := grpcclient.Dial(ctx, tc.address)
			if err != nil {
				return
			}
			defer conn.Close()

			rpcCtx, rpcCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer rpcCancel()

			_, err = unaryEcho(rpcCtx, conn, "should-fail")
			if err == nil {
				t.Fatalf("expected RPC failure for %q", tc.address)
			}
		})
	}
}

func TestDialWebSocketErrorCases(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := grpcclient.DialWebSocket(ctx, "http://127.0.0.1:1234/grpc"); err == nil {
		t.Fatal("expected error for invalid websocket URI scheme")
	}

	if _, err := grpcclient.DialWebSocket(ctx, "ws://127.0.0.1:1/grpc"); err == nil {
		t.Fatal("expected dial failure for unreachable websocket server")
	}
}

func TestDialStdioIntegration(t *testing.T) {
	binaryPath, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	conn, cmd, err := grpcclient.DialStdio(ctx, binaryPath)
	if err != nil {
		t.Fatalf("DialStdio: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	requireUnaryEchoEventually(t, conn, "stdio-echo")
}

func TestDialStdioInvalidBinary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, _, err := grpcclient.DialStdio(ctx, filepath.Join(t.TempDir(), "missing-binary")); err == nil {
		t.Fatal("expected DialStdio to fail for a missing binary")
	}
}

func TestDialStdioServerDidNotStart(t *testing.T) {
	scriptPath := filepath.Join(t.TempDir(), "exit-immediately.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write test script: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, _, err := grpcclient.DialStdio(ctx, scriptPath)
	if err == nil {
		t.Fatal("expected DialStdio startup failure")
	}
	if !strings.Contains(err.Error(), "server did not start") {
		t.Fatalf("expected startup failure, got: %v", err)
	}
}

func TestDialStdioStartupTimeout(t *testing.T) {
	scriptPath := filepath.Join(t.TempDir(), "sleep.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatalf("write test script: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, _, err := grpcclient.DialStdio(ctx, scriptPath)
	if err == nil {
		t.Fatal("expected DialStdio timeout")
	}
	if !strings.Contains(err.Error(), "server startup timeout") {
		t.Fatalf("expected startup timeout, got: %v", err)
	}
}

func startEchoGRPCServer(t *testing.T, lis net.Listener) {
	t.Helper()

	srv := grpc.NewServer()
	testgrpc.RegisterTestServiceServer(srv, &echoTestServer{})

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(lis)
	}()

	t.Cleanup(func() {
		srv.GracefulStop()
		_ = lis.Close()
		select {
		case err := <-errCh:
			if err != nil && !errors.Is(err, grpc.ErrServerStopped) &&
				!strings.Contains(err.Error(), "use of closed network connection") {
				t.Fatalf("server exit error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for gRPC server shutdown")
		}
	})
}

func requireUnaryEchoEventually(t *testing.T, conn *grpc.ClientConn, msg string) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	var lastErr error

	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		out, err := unaryEcho(ctx, conn, msg)
		cancel()
		if err == nil {
			if out != msg {
				t.Fatalf("echo mismatch: got %q want %q", out, msg)
			}
			return
		}
		lastErr = err
		time.Sleep(30 * time.Millisecond)
	}

	t.Fatalf("echo RPC did not succeed before deadline: %v", lastErr)
}

func unaryEcho(ctx context.Context, conn *grpc.ClientConn, msg string) (string, error) {
	client := testgrpc.NewTestServiceClient(conn)
	resp, err := client.UnaryCall(ctx, &testgrpc.SimpleRequest{
		Payload: &testgrpc.Payload{
			Type: testgrpc.PayloadType_COMPRESSABLE,
			Body: []byte(msg),
		},
	})
	if err != nil {
		return "", err
	}
	return string(resp.GetPayload().GetBody()), nil
}
