package serve_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/Organic-Programming/go-holons/pkg/grpcclient"
	"github.com/Organic-Programming/go-holons/pkg/serve"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	testgrpc "google.golang.org/grpc/interop/grpc_testing"
	reflectionv1alpha "google.golang.org/grpc/reflection/grpc_reflection_v1alpha"
	"google.golang.org/grpc/status"
)

type serveEchoServer struct {
	testgrpc.UnimplementedTestServiceServer
}

func (s *serveEchoServer) EmptyCall(context.Context, *testgrpc.Empty) (*testgrpc.Empty, error) {
	return &testgrpc.Empty{}, nil
}

func (s *serveEchoServer) UnaryCall(_ context.Context, in *testgrpc.SimpleRequest) (*testgrpc.SimpleResponse, error) {
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

func TestServeHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_SERVE_HELPER") != "1" {
		t.Skip("serve helper process")
	}

	helperArgs := helperProcessArgs(os.Args)
	if len(helperArgs) < 3 {
		fmt.Fprintf(os.Stderr, "serve helper expects: <mode> <listen-uri> <reflect>\n")
		os.Exit(2)
	}

	mode := helperArgs[0]
	listenURI := helperArgs[1]
	reflectEnabled, err := strconv.ParseBool(helperArgs[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid reflect value %q: %v\n", helperArgs[2], err)
		os.Exit(2)
	}

	register := func(s *grpc.Server) {
		testgrpc.RegisterTestServiceServer(s, &serveEchoServer{})
	}

	switch mode {
	case "run":
		err = serve.Run(listenURI, register)
	case "run-with-options":
		err = serve.RunWithOptions(listenURI, register, reflectEnabled)
	default:
		fmt.Fprintf(os.Stderr, "unknown helper mode %q\n", mode)
		os.Exit(2)
	}

	if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	os.Exit(0)
}

func TestRunServesGRPCOnRandomPort(t *testing.T) {
	port := freeTCPPort(t)
	listenURI := fmt.Sprintf("tcp://127.0.0.1:%d", port)
	address := fmt.Sprintf("127.0.0.1:%d", port)

	cmd, logs := startServeProcess(t, "run", listenURI, true)
	defer stopServeProcess(t, cmd, logs)

	conn := dialServeAndWait(t, address)
	defer conn.Close()

	requireUnaryEchoEventually(t, conn, "serve-run")
}

func TestParseFlags(t *testing.T) {
	testCases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "listen-flag",
			args: []string{"--listen", "unix:///tmp/holon.sock"},
			want: "unix:///tmp/holon.sock",
		},
		{
			name: "legacy-port-flag",
			args: []string{"--port", "7070"},
			want: "tcp://:7070",
		},
		{
			name: "default",
			args: []string{"--unknown", "value"},
			want: "tcp://:9090",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := serve.ParseFlags(tc.args); got != tc.want {
				t.Fatalf("ParseFlags(%v) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

func TestRunWithOptionsReflectionToggle(t *testing.T) {
	testCases := []struct {
		name            string
		reflectEnabled  bool
		expectSupported bool
	}{
		{name: "reflection-enabled", reflectEnabled: true, expectSupported: true},
		{name: "reflection-disabled", reflectEnabled: false, expectSupported: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			port := freeTCPPort(t)
			listenURI := fmt.Sprintf("tcp://127.0.0.1:%d", port)
			address := fmt.Sprintf("127.0.0.1:%d", port)

			cmd, logs := startServeProcess(t, "run-with-options", listenURI, tc.reflectEnabled)
			defer stopServeProcess(t, cmd, logs)

			conn := dialServeAndWait(t, address)
			defer conn.Close()

			requireUnaryEchoEventually(t, conn, tc.name)
			requireReflectionState(t, conn, tc.expectSupported)
		})
	}
}

func TestRunGracefulShutdownOnContextCancellation(t *testing.T) {
	port := freeTCPPort(t)
	listenURI := fmt.Sprintf("tcp://127.0.0.1:%d", port)
	address := fmt.Sprintf("127.0.0.1:%d", port)

	cmd, logs := startServeProcess(t, "run-with-options", listenURI, false)
	conn := dialServeAndWait(t, address)
	_ = conn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	signalErr := make(chan error, 1)
	go func() {
		<-ctx.Done()
		signalErr <- cmd.Process.Signal(syscall.SIGTERM)
	}()

	cancel()
	if err := <-signalErr; err != nil {
		stopServeProcess(t, cmd, logs)
		t.Fatalf("signal serve helper: %v", err)
	}

	if err := waitProcessExit(cmd, 5*time.Second); err != nil {
		stopServeProcess(t, cmd, logs)
		t.Fatalf("serve helper did not exit gracefully: %v\nlogs:\n%s", err, logs.String())
	}

	ctxDial, cancelDial := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancelDial()
	postConn, err := grpcclient.Dial(ctxDial, address)
	if err == nil {
		defer postConn.Close()
		rpcCtx, rpcCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer rpcCancel()
		if _, rpcErr := unaryEcho(rpcCtx, postConn, "after-stop"); rpcErr == nil {
			t.Fatalf("expected RPC to fail after graceful shutdown")
		}
	}
}

func startServeProcess(t *testing.T, mode, listenURI string, reflectEnabled bool) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()

	cmd := exec.Command(os.Args[0], "-test.run=TestServeHelperProcess", "--", mode, listenURI, strconv.FormatBool(reflectEnabled))
	cmd.Env = append(os.Environ(), "GO_WANT_SERVE_HELPER=1")

	var logs bytes.Buffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs

	if err := cmd.Start(); err != nil {
		t.Fatalf("start serve helper: %v", err)
	}

	return cmd, &logs
}

func stopServeProcess(t *testing.T, cmd *exec.Cmd, logs *bytes.Buffer) {
	t.Helper()

	if cmd == nil || cmd.Process == nil {
		return
	}

	_ = cmd.Process.Signal(syscall.SIGTERM)
	if err := waitProcessExit(cmd, 4*time.Second); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("stop serve helper: %v\nlogs:\n%s", err, logs.String())
	}
}

func waitProcessExit(cmd *exec.Cmd, timeout time.Duration) error {
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	select {
	case err := <-waitCh:
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return fmt.Errorf("process exited with status %d", exitErr.ExitCode())
			}
			return err
		}
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("timeout waiting for process exit")
	}
}

func helperProcessArgs(args []string) []string {
	for i, arg := range args {
		if arg == "--" {
			if i+1 < len(args) {
				return args[i+1:]
			}
			return nil
		}
	}
	return nil
}

func freeTCPPort(t *testing.T) int {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate free port: %v", err)
	}
	defer lis.Close()

	addr, ok := lis.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("unexpected listener addr type: %T", lis.Addr())
	}
	return addr.Port
}

func dialServeAndWait(t *testing.T, address string) *grpc.ClientConn {
	t.Helper()

	deadline := time.Now().Add(6 * time.Second)
	var lastErr error

	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		conn, err := grpcclient.Dial(ctx, address)
		cancel()
		if err == nil {
			rpcCtx, rpcCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			_, rpcErr := unaryEcho(rpcCtx, conn, "probe")
			rpcCancel()
			if rpcErr == nil {
				return conn
			}
			lastErr = rpcErr
			_ = conn.Close()
		} else {
			lastErr = err
		}
		time.Sleep(40 * time.Millisecond)
	}

	t.Fatalf("server %s not ready: %v", address, lastErr)
	return nil
}

func requireReflectionState(t *testing.T, conn *grpc.ClientConn, enabled bool) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client := reflectionv1alpha.NewServerReflectionClient(conn)
	stream, err := client.ServerReflectionInfo(ctx)
	if err != nil {
		if !enabled && status.Code(err) == codes.Unimplemented {
			return
		}
		t.Fatalf("reflection stream: %v", err)
	}

	req := &reflectionv1alpha.ServerReflectionRequest{
		MessageRequest: &reflectionv1alpha.ServerReflectionRequest_ListServices{ListServices: "*"},
	}
	if err := stream.Send(req); err != nil {
		if !enabled && status.Code(err) == codes.Unimplemented {
			return
		}
		t.Fatalf("reflection send: %v", err)
	}

	resp, err := stream.Recv()
	if !enabled {
		if status.Code(err) == codes.Unimplemented {
			return
		}
		t.Fatalf("expected reflection to be disabled, got response=%v err=%v", resp, err)
	}
	if err != nil {
		t.Fatalf("reflection recv: %v", err)
	}

	services := resp.GetListServicesResponse().GetService()
	for _, svc := range services {
		if svc.GetName() == "grpc.testing.TestService" {
			return
		}
	}
	t.Fatalf("reflection response missing grpc.testing.TestService")
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
