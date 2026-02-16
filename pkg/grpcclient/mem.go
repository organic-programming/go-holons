package grpcclient

import (
	"context"
	"net"

	"github.com/organic-programming/go-holons/pkg/transport"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// DialMem connects to an in-process gRPC server via a MemListener.
// Both server and client live in the same process — zero network overhead.
func DialMem(ctx context.Context, mem *transport.MemListener) (*grpc.ClientConn, error) {
	return grpc.NewClient(
		"passthrough:///mem",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return mem.Dial()
		}),
	)
}
