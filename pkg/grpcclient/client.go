// Package grpcclient provides client-side gRPC helpers for inter-holon
// communication. It supports TCP, Unix sockets, and stdio pipe transports.
//
// Dial connects to an existing gRPC server:
//
//	conn, err := grpcclient.Dial(ctx, "localhost:9090")     // TCP
//	conn, err := grpcclient.Dial(ctx, "unix:///tmp/h.sock") // Unix
//
// DialStdio launches a holon binary, communicates via stdin/stdout pipes,
// and returns the gRPC connection:
//
//	conn, cmd, err := grpcclient.DialStdio(ctx, "/path/to/holon")
//	defer cmd.Process.Kill()
//	defer conn.Close()
package grpcclient

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Dial connects to a gRPC server at the given address.
// For TCP: "host:port". For Unix: "unix:///path".
func Dial(ctx context.Context, address string) (*grpc.ClientConn, error) {
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}

	if isUnixAddress(address) {
		path := address
		if len(path) > 7 && path[:7] == "unix://" {
			path = path[7:]
		}
		opts = append(opts, grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return net.DialTimeout("unix", path, 5*time.Second)
		}))
		address = "passthrough:///unix"
	}

	return grpc.NewClient(address, opts...)
}

// DialStdio launches a holon binary with `serve --listen stdio://` and
// returns a gRPC connection backed by the process's stdin/stdout pipes.
// The caller must kill the process and close the connection when done.
//
// This is the purest form of inter-holon communication: no port allocation,
// no socket files, no network stack — just a pipe. Inspired by LSP.
//
// Startup detection reads the first byte emitted on stdout, which confirms
// the child gRPC server has started producing HTTP/2 traffic. The byte is
// then re-inserted into the stream so no protocol data is lost.
func DialStdio(ctx context.Context, binaryPath string) (*grpc.ClientConn, *exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, binaryPath, "serve", "--listen", "stdio://")

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("create stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start %s: %w", binaryPath, err)
	}

	// Wait for the server's HTTP/2 SETTINGS frame. Reading the first
	// byte proves the gRPC server is alive and the pipe is functional.
	firstByte := make([]byte, 1)
	readCh := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(stdoutPipe, firstByte)
		readCh <- err
	}()
	select {
	case err := <-readCh:
		if err != nil {
			cmd.Process.Kill() //nolint:errcheck
			cmd.Wait()         //nolint:errcheck
			return nil, nil, fmt.Errorf("server did not start: %w", err)
		}
	case <-ctx.Done():
		cmd.Process.Kill() //nolint:errcheck
		cmd.Wait()         //nolint:errcheck
		return nil, nil, fmt.Errorf("server startup timeout")
	}

	// Prepend the consumed byte back into the reader stream.
	pConn := &pipeConn{
		reader: io.MultiReader(bytes.NewReader(firstByte), stdoutPipe),
		writer: stdinPipe,
	}

	// Single-connection dialer: the pipe can only be used once.
	var dialOnce sync.Once
	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		var conn net.Conn
		dialOnce.Do(func() { conn = pConn })
		if conn == nil {
			return nil, fmt.Errorf("stdio pipe already consumed")
		}
		return conn, nil
	}

	// DialContext+WithBlock forces immediate HTTP/2 handshake,
	// which is required for single-connection transports.
	//nolint:staticcheck // DialContext is deprecated but needed for pipes.
	conn, err := grpc.DialContext(ctx,
		"passthrough:///stdio",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(dialer),
		grpc.WithBlock(),
	)
	if err != nil {
		cmd.Process.Kill() //nolint:errcheck
		cmd.Wait()         //nolint:errcheck
		return nil, nil, fmt.Errorf("grpc handshake over stdio: %w", err)
	}

	return conn, cmd, nil
}

func isUnixAddress(addr string) bool {
	return len(addr) > 7 && addr[:7] == "unix://"
}

// --- pipeConn: wraps stdin/stdout pipes as a net.Conn ---

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
