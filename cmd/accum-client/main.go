package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	accumv1 "github.com/organic-programming/go-holons/pkg/accum/v1"
)

const defaultDialTimeout = 5 * time.Second

func main() {
	target := flag.String("target", "", "server URI (gRPC URI or Holon-RPC ws://.../rpc)")
	counter := flag.Int("counter", 0, "initial relay counter")
	traceCSV := flag.String("trace", "", "comma-separated initial trace")
	next := flag.String("next", "", "next hop address (URI or method string)")

	method := flag.String("method", accumv1.MethodRelay, "Holon-RPC method to invoke")
	targetPeer := flag.String("target-peer", "", "Holon-RPC target peer id for unicast routing")

	timeoutMs := flag.Int("timeout-ms", int(defaultDialTimeout/time.Millisecond), "dial+invoke timeout in milliseconds")
	goBinary := flag.String("go", defaultGoBinary(), "go binary used to spawn stdio accum-server")
	stdioBin := flag.String("stdio-bin", strings.TrimSpace(os.Getenv("HOLONS_ACCUM_SERVER_BIN")), "accum-server binary path used for stdio://")
	flag.Parse()

	uri := strings.TrimSpace(*target)
	if uri == "" && flag.NArg() >= 1 {
		uri = strings.TrimSpace(flag.Arg(0))
	}
	uri = normalizeURI(uri)

	if uri == "" {
		fmt.Fprintln(os.Stderr, "usage: go run ./cmd/accum-client --target <URI> [--counter N] [--trace A,B] [--next NEXT]")
		os.Exit(2)
	}

	timeout := time.Duration(*timeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultDialTimeout
	}

	req := &accumv1.RelayRequest{
		Counter: int32(*counter),
		Trace:   parseTrace(*traceCSV),
		Next:    *next,
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var (
		resp *accumv1.RelayResponse
		err  error
	)

	if accumv1.IsHolonRPCURI(uri) {
		resp, err = accumv1.InvokeRelayHolonRPC(ctx, uri, *method, req, *targetPeer)
	} else {
		resp, err = accumv1.InvokeRelayGRPC(
			ctx,
			uri,
			req,
			accumv1.DialOptions{
				GoBinary:    strings.TrimSpace(*goBinary),
				StdioBinary: strings.TrimSpace(*stdioBin),
			},
		)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "invoke failed: %v\n", err)
		os.Exit(1)
	}

	out := map[string]any{
		"counter": resp.Counter,
		"trace":   resp.Trace,
	}
	enc := json.NewEncoder(os.Stdout)
	_ = enc.Encode(out)
}

func parseTrace(csv string) []string {
	csv = strings.TrimSpace(csv)
	if csv == "" {
		return nil
	}

	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func defaultGoBinary() string {
	if fromEnv := strings.TrimSpace(os.Getenv("GO_BIN")); fromEnv != "" {
		return fromEnv
	}
	return "go"
}

func normalizeURI(uri string) string {
	if uri == "stdio" {
		return "stdio://"
	}
	return uri
}
