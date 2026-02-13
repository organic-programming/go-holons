package transport

import (
	"net"

	"google.golang.org/grpc/test/bufconn"
)

const memBufSize = 1024 * 1024 // 1MB buffer

// MemListener wraps bufconn.Listener and exposes a Dial method for
// client-side connections. Exported so tests and composite holons can
// create a server and client in the same process.
type MemListener struct {
	*bufconn.Listener
}

// NewMemListener creates an in-process listener backed by an in-memory
// buffer. Use Dial() to connect from the same process.
func NewMemListener() *MemListener {
	return &MemListener{Listener: bufconn.Listen(memBufSize)}
}

// Dial creates a client-side connection to this in-process listener.
// Pass this to grpc.WithContextDialer.
func (m *MemListener) Dial() (net.Conn, error) {
	return m.Listener.Dial()
}

// DialContext matches the grpc.WithContextDialer signature.
func (m *MemListener) DialContext(_ interface{}, _ string) (net.Conn, error) {
	return m.Listener.Dial()
}

// Addr returns the canonical in-memory listener address.
func (m *MemListener) Addr() net.Addr {
	return memAddr{}
}

type memAddr struct{}

func (memAddr) Network() string { return "mem" }
func (memAddr) String() string  { return "mem://" }
