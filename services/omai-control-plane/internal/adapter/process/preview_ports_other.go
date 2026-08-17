//go:build !linux

package process

import (
	"fmt"
	"net"
	"strconv"
	"time"
)

func ownedListenerPorts(int) ([]uint32, error) {
	return nil, fmt.Errorf("process-owned listener discovery is unavailable on this platform")
}

// Development on non-Linux hosts retains preferred-port readiness. Production
// is Linux-only and uses process-owned /proc attribution above.
func fallbackPreviewPort(preferred []uint32) uint32 {
	for _, portNumber := range preferred {
		connection, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(portNumber))), 100*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return portNumber
		}
	}
	return 0
}
