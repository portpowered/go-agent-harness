package endpoint

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/portpowered/go-agent-harness/go-agent-runtime/services/devices"
)

// ValidateRemoteEndpoint checks the service-owned loopback endpoint policy
// without opening a device registry or media worker.
func ValidateRemoteEndpoint(endpoint string) error {
	value := strings.TrimSpace(endpoint)
	if value == "" {
		return nil
	}
	if strings.Contains(value, "://") {
		return fmt.Errorf("%w: want loopback host:port", devices.ErrInvalidRemoteEndpoint)
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil || port == "" {
		return fmt.Errorf("%w: want loopback host:port, got %q", devices.ErrInvalidRemoteEndpoint, value)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber <= 0 || portNumber > 65535 {
		return fmt.Errorf("%w: port %q is invalid", devices.ErrInvalidRemoteEndpoint, port)
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("%w: host %q is not loopback", devices.ErrInvalidRemoteEndpoint, host)
		}
	}
	return nil
}
