package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	accumv1 "github.com/organic-programming/go-holons/pkg/accum/v1"
	"github.com/organic-programming/go-holons/pkg/holonrpc"
	"github.com/organic-programming/go-holons/pkg/transport"
	"google.golang.org/grpc"
)

const (
	defaultListenURI = "tcp://127.0.0.1:0"
	defaultSDK       = "go-holons"
)

type options struct {
	listenURI string
	sdk       string

	rpcForwardURL string
	rpcTargetPeer string

	goBinary string
	stdioBin string
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		// Compatibility with grpcclient.DialStdio helper processes.
		os.Args = append([]string{os.Args[0]}, os.Args[2:]...)
	}

	opts := parseFlags()

	svc := &accumv1.Service{
		SDKName: opts.sdk,
		ForwardGRPC: func(ctx context.Context, uri string, req *accumv1.RelayRequest) (*accumv1.RelayResponse, error) {
			return accumv1.InvokeRelayGRPC(
				ctx,
				uri,
				req,
				accumv1.DialOptions{
					GoBinary:    opts.goBinary,
					StdioBinary: opts.stdioBin,
				},
			)
		},
	}

	if strings.TrimSpace(opts.rpcForwardURL) != "" {
		svc.ForwardHolonRPC = func(ctx context.Context, method string, req *accumv1.RelayRequest) (*accumv1.RelayResponse, error) {
			return accumv1.InvokeRelayHolonRPC(
				ctx,
				opts.rpcForwardURL,
				method,
				req,
				opts.rpcTargetPeer,
			)
		}
	}

	if accumv1.IsHolonRPCURI(opts.listenURI) {
		if err := runHolonRPCServer(opts.listenURI, svc); err != nil {
			fmt.Fprintf(os.Stderr, "serve failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := runGRPCServer(opts.listenURI, svc); err != nil {
		fmt.Fprintf(os.Stderr, "serve failed: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags() options {
	listen := flag.String("listen", defaultListenURI, "transport URI to listen on")
	port := flag.String("port", "", "tcp port shortcut (equivalent to --listen tcp://127.0.0.1:<port>)")
	sdk := flag.String("sdk", defaultSDK, "sdk identifier in Relay trace")
	sdkName := flag.String("sdk-name", "", "alias of --sdk")

	rpcForwardURL := flag.String("rpc-forward-url", strings.TrimSpace(os.Getenv("HOLONS_ACCUM_RPC_FORWARD_URL")), "Holon-RPC server URL used when forwarding method-based next hops")
	rpcTargetPeer := flag.String("rpc-target-peer", strings.TrimSpace(os.Getenv("HOLONS_ACCUM_RPC_TARGET_PEER")), "Holon-RPC target peer ID used for forwarded unicast calls")

	goBinary := flag.String("go", defaultGoBinary(), "go binary used to spawn stdio accum server for gRPC forwarding")
	stdioBin := flag.String("stdio-bin", strings.TrimSpace(os.Getenv("HOLONS_ACCUM_SERVER_BIN")), "accum-server binary path used for stdio:// gRPC forwarding")

	flag.Parse()

	listenURI := *listen
	if *port != "" {
		listenURI = fmt.Sprintf("tcp://127.0.0.1:%s", *port)
	}

	effectiveSDK := *sdk
	if strings.TrimSpace(*sdkName) != "" {
		effectiveSDK = strings.TrimSpace(*sdkName)
	}

	return options{
		listenURI: listenURI,
		sdk:       effectiveSDK,

		rpcForwardURL: *rpcForwardURL,
		rpcTargetPeer: *rpcTargetPeer,

		goBinary: *goBinary,
		stdioBin: *stdioBin,
	}
}

func runGRPCServer(listenURI string, svc *accumv1.Service) error {
	lis, err := transport.Listen(listenURI)
	if err != nil {
		return fmt.Errorf("listen failed: %w", err)
	}
	defer lis.Close()

	grpcServer := grpc.NewServer()
	accumv1.RegisterAccumServer(grpcServer, svc)

	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- grpcServer.Serve(lis)
	}()

	if !isStdioURI(listenURI) {
		fmt.Println(publicURI(listenURI, lis.Addr()))
	}

	if isStdioURI(listenURI) {
		if err := <-serveErrCh; err != nil && !isBenignServeError(err) {
			return err
		}
		return nil
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigCh)

	select {
	case <-sigCh:
		shutdown(grpcServer)
	case err := <-serveErrCh:
		if err != nil && !isBenignServeError(err) {
			return err
		}
		return nil
	}

	if err := <-serveErrCh; err != nil && !isBenignServeError(err) {
		return err
	}
	return nil
}

func runHolonRPCServer(listenURI string, svc *accumv1.Service) error {
	server := holonrpc.NewServer(listenURI)
	server.Register(accumv1.MethodRelay, svc.HolonRPCHandler)

	addr, err := server.Start()
	if err != nil {
		return err
	}
	fmt.Println(addr)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigCh)

	<-sigCh

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return server.Close(ctx)
}

func defaultGoBinary() string {
	if fromEnv := strings.TrimSpace(os.Getenv("GO_BIN")); fromEnv != "" {
		return fromEnv
	}
	return "go"
}

func shutdown(server *grpc.Server) {
	done := make(chan struct{})
	go func() {
		server.GracefulStop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		server.Stop()
	}
}

func publicURI(listenURI string, addr net.Addr) string {
	if strings.HasPrefix(listenURI, "tcp://") {
		host := extractTCPHost(listenURI)
		if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
			host = "127.0.0.1"
		}

		_, port, err := net.SplitHostPort(addr.String())
		if err != nil {
			return fmt.Sprintf("tcp://%s", addr.String())
		}
		return fmt.Sprintf("tcp://%s:%s", host, port)
	}
	return listenURI
}

func extractTCPHost(uri string) string {
	rest := strings.TrimPrefix(uri, "tcp://")
	host, _, err := net.SplitHostPort(rest)
	if err != nil {
		return ""
	}
	return host
}

func isStdioURI(uri string) bool {
	return uri == "stdio://" || uri == "stdio"
}

func isBenignServeError(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, grpc.ErrServerStopped) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "use of closed network connection")
}
