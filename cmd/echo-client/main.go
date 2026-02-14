package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const defaultDialTimeout = 5 * time.Second

type PingRequest struct {
	Message string `json:"message"`
}

type PingResponse struct {
	Message string `json:"message"`
	SDK     string `json:"sdk"`
	Version string `json:"version"`
}

type jsonCodec struct{}

func (jsonCodec) Name() string { return "json" }

func (jsonCodec) Marshal(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}

func (jsonCodec) Unmarshal(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}

func main() {
	sdk := flag.String("sdk", "go-holons", "sdk name")
	serverSDK := flag.String("server-sdk", "unknown", "expected remote sdk name")
	message := flag.String("message", "hello", "Ping request message")
	timeoutMs := flag.Int("timeout-ms", int(defaultDialTimeout/time.Millisecond), "dial+invoke timeout in milliseconds")
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: go run ./cmd/echo-client [--sdk name] [--server-sdk name] [--message hello] tcp://host:port")
		os.Exit(2)
	}

	uri := flag.Arg(0)
	target, dialer, err := normalizeTarget(uri)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	timeout := time.Duration(*timeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultDialTimeout
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(jsonCodec{})),
	}
	if dialer != nil {
		opts = append(opts, grpc.WithContextDialer(dialer))
	}

	//nolint:staticcheck // DialContext is required with custom dialers + blocking connect.
	conn, err := grpc.DialContext(ctx, target, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dial failed: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	started := time.Now()
	var out PingResponse
	err = conn.Invoke(ctx, "/echo.v1.Echo/Ping", &PingRequest{Message: *message}, &out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invoke failed: %v\n", err)
		os.Exit(1)
	}
	if out.Message != *message {
		fmt.Fprintf(os.Stderr, "unexpected echo message: %q\n", out.Message)
		os.Exit(1)
	}

	result := map[string]interface{}{
		"status":       "pass",
		"sdk":          *sdk,
		"server_sdk":   *serverSDK,
		"latency_ms":   time.Since(started).Milliseconds(),
		"response_sdk": out.SDK,
	}

	enc := json.NewEncoder(os.Stdout)
	_ = enc.Encode(result)
}

func normalizeTarget(uri string) (string, func(context.Context, string) (net.Conn, error), error) {
	if strings.HasPrefix(uri, "tcp://") {
		return strings.TrimPrefix(uri, "tcp://"), nil, nil
	}
	if strings.HasPrefix(uri, "unix://") {
		path := strings.TrimPrefix(uri, "unix://")
		dialer := func(_ context.Context, _ string) (net.Conn, error) {
			return net.DialTimeout("unix", path, 5*time.Second)
		}
		return "passthrough:///unix", dialer, nil
	}

	return "", nil, fmt.Errorf("unsupported URI: %s", uri)
}
