package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Organic-Programming/go-holons/pkg/grpcclient"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestEchoServer_TCP_RoundTrip(t *testing.T) {
	port := freeTCPPort(t)
	listenURI := fmt.Sprintf("tcp://127.0.0.1:%d", port)
	address := fmt.Sprintf("127.0.0.1:%d", port)

	cmd, logs := startEchoServerProcess(t, listenURI)
	defer stopEchoServerProcess(t, cmd, logs)

	conn := dialEchoEventually(t, address, nil)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := pingEcho(ctx, conn, "hello-tcp")
	if err != nil {
		t.Fatalf("ping tcp: %v", err)
	}
	if out.Message != "hello-tcp" {
		t.Fatalf("tcp message = %q, want %q", out.Message, "hello-tcp")
	}
	if out.SDK != defaultSDK {
		t.Fatalf("tcp sdk = %q, want %q", out.SDK, defaultSDK)
	}
}

func TestEchoServer_Unix_RoundTrip(t *testing.T) {
	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("echo-server-%d.sock", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.Remove(socketPath) })
	listenURI := "unix://" + socketPath

	cmd, logs := startEchoServerProcess(t, listenURI)
	defer stopEchoServerProcess(t, cmd, logs)

	dialer := func(_ context.Context, _ string) (net.Conn, error) {
		return net.DialTimeout("unix", socketPath, time.Second)
	}
	conn := dialEchoEventually(t, "passthrough:///unix", dialer)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := pingEcho(ctx, conn, "hello-unix")
	if err != nil {
		t.Fatalf("ping unix: %v", err)
	}
	if out.Message != "hello-unix" {
		t.Fatalf("unix message = %q, want %q", out.Message, "hello-unix")
	}
}

func TestEchoServer_Stdio_RoundTrip(t *testing.T) {
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

	callCtx, callCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer callCancel()
	out, err := pingEcho(callCtx, conn, "hello-stdio")
	if err != nil {
		t.Fatalf("ping stdio: %v", err)
	}
	if out.Message != "hello-stdio" {
		t.Fatalf("stdio message = %q, want %q", out.Message, "hello-stdio")
	}
}

func TestEchoServer_InvalidListenURI(t *testing.T) {
	testCases := []string{
		"bad://example",
		"wss://127.0.0.1:0/grpc",
	}

	for _, listenURI := range testCases {
		listenURI := listenURI
		t.Run(listenURI, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "serve", "--listen", listenURI)
			var output bytes.Buffer
			cmd.Stdout = &output
			cmd.Stderr = &output

			err := cmd.Run()
			if err == nil {
				t.Fatalf("expected listen failure for %s", listenURI)
			}
			if !strings.Contains(output.String(), "listen failed") {
				t.Fatalf("expected listen failure message, got:\n%s", output.String())
			}
		})
	}
}

func TestEchoServer_StdioEOFExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, os.Args[0], "serve", "--listen", "stdio://")
	cmd.Stdin = strings.NewReader("")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("stdio server did not exit before timeout:\n%s", output.String())
	}
	if err == nil {
		t.Fatalf("expected stdio EOF startup to fail, output:\n%s", output.String())
	}
	if !strings.Contains(output.String(), "serve failed: EOF") {
		t.Fatalf("expected EOF failure message, got:\n%s", output.String())
	}
}

func TestParseFlags(t *testing.T) {
	opts := parseFlagsWithArgs(t, []string{"echo-server", "--listen", "tcp://127.0.0.1:9999", "--sdk", "x", "--version", "y"})
	if opts.listenURI != "tcp://127.0.0.1:9999" {
		t.Fatalf("listenURI = %q", opts.listenURI)
	}
	if opts.sdk != "x" {
		t.Fatalf("sdk = %q", opts.sdk)
	}
	if opts.version != "y" {
		t.Fatalf("version = %q", opts.version)
	}

	opts = parseFlagsWithArgs(t, []string{"echo-server", "--port", "7001"})
	if opts.listenURI != "tcp://127.0.0.1:7001" {
		t.Fatalf("listenURI from --port = %q", opts.listenURI)
	}
}

func TestServerHelpers(t *testing.T) {
	s := server{sdk: "sdk-x", version: "1.2.3"}
	resp, err := s.Ping(context.Background(), &PingRequest{Message: "hola"})
	if err != nil {
		t.Fatalf("Ping returned error: %v", err)
	}
	if resp.Message != "hola" || resp.SDK != "sdk-x" || resp.Version != "1.2.3" {
		t.Fatalf("unexpected ping response: %#v", resp)
	}

	if got := extractTCPHost("tcp://127.0.0.1:9090"); got != "127.0.0.1" {
		t.Fatalf("extractTCPHost = %q", got)
	}
	if got := extractTCPHost("tcp://bad"); got != "" {
		t.Fatalf("extractTCPHost invalid = %q, want empty", got)
	}
	if !isStdioURI("stdio://") || !isStdioURI("stdio") {
		t.Fatal("expected stdio URIs to be recognized")
	}
	if isStdioURI("tcp://127.0.0.1:9090") {
		t.Fatal("unexpected stdio detection for tcp URI")
	}
	if !isBenignServeError(nil) || !isBenignServeError(grpc.ErrServerStopped) {
		t.Fatal("expected benign errors")
	}
	if !isBenignServeError(errors.New("use of closed network connection")) {
		t.Fatal("expected closed network connection to be benign")
	}
	if isBenignServeError(errors.New("boom")) {
		t.Fatal("unexpected benign classification for generic error")
	}

	addr := &net.TCPAddr{IP: net.ParseIP("0.0.0.0"), Port: 1234}
	if got := publicURI("tcp://0.0.0.0:0", addr); got != "tcp://127.0.0.1:1234" {
		t.Fatalf("publicURI wildcard = %q", got)
	}
	if got := publicURI("unix:///tmp/x.sock", addr); got != "unix:///tmp/x.sock" {
		t.Fatalf("publicURI unix = %q", got)
	}

	grpcServer := grpc.NewServer()
	done := make(chan struct{}, 1)
	go func() {
		shutdown(grpcServer)
		done <- struct{}{}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not return")
	}
}

func parseFlagsWithArgs(t *testing.T, args []string) options {
	t.Helper()

	oldArgs := os.Args
	oldFlags := flag.CommandLine
	defer func() {
		os.Args = oldArgs
		flag.CommandLine = oldFlags
	}()

	os.Args = args
	flag.CommandLine = flag.NewFlagSet(args[0], flag.ContinueOnError)
	flag.CommandLine.SetOutput(ioDiscard{})

	return parseFlags()
}

func startEchoServerProcess(t *testing.T, listenURI string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()

	cmd := exec.Command(os.Args[0], "serve", "--listen", listenURI, "--sdk", defaultSDK, "--version", defaultVersion)
	var logs bytes.Buffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start echo-server process: %v", err)
	}
	return cmd, &logs
}

func stopEchoServerProcess(t *testing.T, cmd *exec.Cmd, logs *bytes.Buffer) {
	t.Helper()

	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	select {
	case err := <-waitCh:
		if err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 0 {
				t.Fatalf("echo-server exit error: %v\nlogs:\n%s", err, logs.String())
			}
		}
	case <-time.After(4 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("timeout stopping echo-server\nlogs:\n%s", logs.String())
	}
}

func dialEchoEventually(t *testing.T, target string, dialer func(context.Context, string) (net.Conn, error)) *grpc.ClientConn {
	t.Helper()

	deadline := time.Now().Add(6 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		opts := []grpc.DialOption{
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithBlock(),
			grpc.WithDefaultCallOptions(grpc.ForceCodec(jsonCodec{})),
		}
		if dialer != nil {
			opts = append(opts, grpc.WithContextDialer(dialer))
		}
		//nolint:staticcheck // Required for blocking connect with custom dialers.
		conn, err := grpc.DialContext(ctx, target, opts...)
		cancel()
		if err == nil {
			return conn
		}
		lastErr = err
		time.Sleep(40 * time.Millisecond)
	}

	t.Fatalf("dial target %s failed: %v", target, lastErr)
	return nil
}

func pingEcho(ctx context.Context, conn *grpc.ClientConn, msg string) (*PingResponse, error) {
	var out PingResponse
	err := conn.Invoke(ctx, "/echo.v1.Echo/Ping", &PingRequest{Message: msg}, &out, grpc.ForceCodec(jsonCodec{}))
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func freeTCPPort(t *testing.T) int {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate free port: %v", err)
	}
	defer lis.Close()
	return lis.Addr().(*net.TCPAddr).Port
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
