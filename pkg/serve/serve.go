// Package serve provides a standard implementation of the `serve` command
// for Go holons (Constitution Article 11).
//
// Usage in a holon's main.go:
//
//	case "serve":
//	    listenURI := serve.ParseFlags(os.Args[2:])
//	    serve.Run(listenURI, func(s *grpc.Server) {
//	        pb.RegisterMyServiceServer(s, &myServer{})
//	    })
package serve

import (
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Organic-Programming/go-holons/pkg/transport"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

// RegisterFunc is called to register gRPC services on the server.
type RegisterFunc func(s *grpc.Server)

// ParseFlags extracts --listen and --port from command-line args.
// Returns a transport URI. If neither flag is present, returns the default.
func ParseFlags(args []string) string {
	for i, arg := range args {
		if arg == "--listen" && i+1 < len(args) {
			return args[i+1]
		}
		// Backward compatibility: --port N → tcp://:N
		if arg == "--port" && i+1 < len(args) {
			return "tcp://:" + args[i+1]
		}
	}
	return transport.DefaultURI
}

// Run starts a gRPC server on the given transport URI with reflection
// enabled. It blocks until SIGTERM/SIGINT is received, then shuts down
// gracefully. If in-flight RPC draining exceeds 10 seconds, the server is
// force-stopped to satisfy the operational shutdown deadline.
// This is the standard entry point for any Go holon's serve.
func Run(listenURI string, register RegisterFunc) error {
	return RunWithOptions(listenURI, register, true)
}

// RunWithOptions is like Run but lets you control reflection.
// It installs SIGTERM/SIGINT handlers, starts graceful draining on signal,
// and force-stops after 10 seconds if draining does not complete.
func RunWithOptions(listenURI string, register RegisterFunc, reflect bool) error {
	lis, err := transport.Listen(listenURI)
	if err != nil {
		return err
	}

	s := grpc.NewServer()
	register(s)

	if reflect {
		reflection.Register(s)
	}

	// Graceful shutdown on SIGTERM/SIGINT
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigCh)
	go func() {
		<-sigCh
		log.Println("shutting down gRPC server...")

		done := make(chan struct{})
		go func() {
			defer close(done)
			s.GracefulStop()
		}()

		select {
		case <-done:
		case <-time.After(10 * time.Second):
			log.Println("graceful stop timed out after 10s; forcing hard stop")
			s.Stop()
		}
	}()

	mode := "reflection ON"
	if !reflect {
		mode = "reflection OFF"
	}
	log.Printf("gRPC server listening on %s (%s)", listenURI, mode)
	return s.Serve(lis)
}
