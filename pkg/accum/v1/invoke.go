package v1

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/organic-programming/go-holons/pkg/grpcclient"
	"github.com/organic-programming/go-holons/pkg/holonrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	defaultDialTimeout = 5 * time.Second
)

// DialOptions controls transport-specific dialing behavior.
type DialOptions struct {
	GoBinary    string
	StdioBinary string
}

// InvokeRelayGRPC dials a gRPC target URI, invokes Relay, and closes resources.
func InvokeRelayGRPC(ctx context.Context, targetURI string, req *RelayRequest, opts DialOptions) (*RelayResponse, error) {
	conn, child, err := DialGRPC(ctx, targetURI, opts)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	defer stopChild(child)

	client := NewAccumClient(conn)
	return client.Relay(ctx, req)
}

// DialGRPC dials a gRPC endpoint URI and returns an optional child process for stdio.
func DialGRPC(ctx context.Context, uri string, opts DialOptions) (*grpc.ClientConn, *exec.Cmd, error) {
	trimmed := strings.TrimSpace(uri)
	if trimmed == "" {
		return nil, nil, fmt.Errorf("dial target is required")
	}

	if isStdioURI(trimmed) {
		return dialStdio(ctx, opts)
	}

	if isWebSocketURI(trimmed) && !IsHolonRPCURI(trimmed) {
		conn, err := grpcclient.DialWebSocket(ctx, trimmed)
		if err != nil {
			return nil, nil, err
		}
		return conn, nil, nil
	}

	target, dialer, err := normalizeGRPCTarget(trimmed)
	if err != nil {
		return nil, nil, err
	}

	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	}
	if dialer != nil {
		dialOpts = append(dialOpts, grpc.WithContextDialer(dialer))
	}

	//nolint:staticcheck // DialContext is needed for custom dialers + blocking connect.
	conn, err := grpc.DialContext(ctx, target, dialOpts...)
	if err != nil {
		return nil, nil, err
	}
	return conn, nil, nil
}

// InvokeRelayHolonRPC dials a Holon-RPC endpoint and invokes Relay.
func InvokeRelayHolonRPC(
	ctx context.Context,
	serverURL string,
	method string,
	req *RelayRequest,
	targetPeer string,
) (*RelayResponse, error) {
	m := strings.TrimSpace(method)
	if m == "" {
		m = MethodRelay
	}

	client := holonrpc.NewClient()
	if err := client.Connect(ctx, serverURL); err != nil {
		return nil, err
	}
	defer client.Close()

	params := RelayRequestToMap(req)
	if tp := strings.TrimSpace(targetPeer); tp != "" {
		// Backward-compatible unicast hints:
		// _target follows PROTOCOL.md, _peer keeps compatibility with current hub code.
		params["_target"] = tp
		params["_peer"] = tp
	}

	out, err := client.Invoke(ctx, m, params)
	if err != nil {
		return nil, err
	}
	return RelayResponseFromMap(out)
}

// IsHolonRPCURI reports whether a URI points to a Holon-RPC WebSocket endpoint.
func IsHolonRPCURI(uri string) bool {
	parsed, err := url.Parse(uri)
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "ws", "wss":
	default:
		return false
	}
	path := strings.TrimSpace(parsed.Path)
	return path == "/rpc" || strings.HasSuffix(path, "/rpc")
}

func isGRPCURI(uri string) bool {
	trimmed := strings.TrimSpace(uri)
	return strings.HasPrefix(trimmed, "tcp://") ||
		strings.HasPrefix(trimmed, "unix://") ||
		isStdioURI(trimmed) ||
		strings.HasPrefix(trimmed, "ws://") ||
		strings.HasPrefix(trimmed, "wss://")
}

func isWebSocketURI(uri string) bool {
	return strings.HasPrefix(uri, "ws://") || strings.HasPrefix(uri, "wss://")
}

func isStdioURI(uri string) bool {
	return uri == "stdio://" || uri == "stdio"
}

func normalizeGRPCTarget(uri string) (string, func(context.Context, string) (net.Conn, error), error) {
	if strings.HasPrefix(uri, "tcp://") {
		return strings.TrimPrefix(uri, "tcp://"), nil, nil
	}

	if strings.HasPrefix(uri, "unix://") {
		path := strings.TrimPrefix(uri, "unix://")
		if strings.TrimSpace(path) == "" {
			return "", nil, fmt.Errorf("unix target is missing socket path")
		}
		dialer := func(_ context.Context, _ string) (net.Conn, error) {
			return net.DialTimeout("unix", path, defaultDialTimeout)
		}
		return "passthrough:///unix", dialer, nil
	}

	if strings.Contains(uri, "://") {
		return "", nil, fmt.Errorf("unsupported gRPC target URI: %s", uri)
	}

	return uri, nil, nil
}

func dialStdio(ctx context.Context, opts DialOptions) (*grpc.ClientConn, *exec.Cmd, error) {
	if stdioBin := strings.TrimSpace(opts.StdioBinary); stdioBin != "" {
		conn, cmd, err := grpcclient.DialStdio(ctx, stdioBin)
		if err != nil {
			return nil, nil, err
		}
		return conn, cmd, nil
	}

	goBinary := strings.TrimSpace(opts.GoBinary)
	if goBinary == "" {
		if env := strings.TrimSpace(os.Getenv("GO_BIN")); env != "" {
			goBinary = env
		} else {
			goBinary = "go"
		}
	}

	cmd := exec.CommandContext(
		ctx,
		goBinary,
		"run",
		"./cmd/accum-server",
		"--listen",
		"stdio://",
	)
	return dialStdioCommand(ctx, cmd)
}

func dialStdioCommand(ctx context.Context, cmd *exec.Cmd) (*grpc.ClientConn, *exec.Cmd, error) {
	cmd.Stderr = os.Stderr

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("create stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start stdio server: %w", err)
	}

	firstByte := make([]byte, 1)
	readCh := make(chan error, 1)
	go func() {
		_, readErr := io.ReadFull(stdoutPipe, firstByte)
		readCh <- readErr
	}()

	select {
	case readErr := <-readCh:
		if readErr != nil {
			killAndWait(cmd)
			return nil, nil, fmt.Errorf("stdio server startup failed: %w", readErr)
		}
	case <-ctx.Done():
		killAndWait(cmd)
		return nil, nil, fmt.Errorf("stdio server startup timeout")
	}

	pConn := &pipeConn{
		reader: io.MultiReader(bytes.NewReader(firstByte), stdoutPipe),
		writer: stdinPipe,
	}

	var dialOnce sync.Once
	dialer := func(context.Context, string) (net.Conn, error) {
		var conn net.Conn
		dialOnce.Do(func() {
			conn = pConn
		})
		if conn == nil {
			return nil, fmt.Errorf("stdio pipe already consumed")
		}
		return conn, nil
	}

	//nolint:staticcheck // DialContext is needed for custom dialers + blocking connect.
	conn, err := grpc.DialContext(
		ctx,
		"passthrough:///stdio",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(dialer),
		grpc.WithBlock(),
	)
	if err != nil {
		killAndWait(cmd)
		return nil, nil, fmt.Errorf("grpc handshake over stdio: %w", err)
	}

	return conn, cmd, nil
}

func stopChild(cmd *exec.Cmd) {
	killAndWait(cmd)
}

func killAndWait(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

type pipeConn struct {
	reader io.Reader
	writer io.WriteCloser
}

func (c *pipeConn) Read(p []byte) (int, error)         { return c.reader.Read(p) }
func (c *pipeConn) Write(p []byte) (int, error)        { return c.writer.Write(p) }
func (c *pipeConn) Close() error                       { return c.writer.Close() }
func (c *pipeConn) LocalAddr() net.Addr                { return pipeAddr{} }
func (c *pipeConn) RemoteAddr() net.Addr               { return pipeAddr{} }
func (c *pipeConn) SetDeadline(_ time.Time) error      { return nil }
func (c *pipeConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *pipeConn) SetWriteDeadline(_ time.Time) error { return nil }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "stdio://" }
