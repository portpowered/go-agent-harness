package fakecodex

import (
	"fmt"
	"sync"
	"time"

	"github.com/pion/logging"
	"github.com/pion/transport/v4/vnet"
	"github.com/pion/webrtc/v4"
)

// Virtual network settings. The ICE timeouts are short so a broken
// connection fails fast instead of holding a test.
const (
	vnetCIDR            = "10.66.0.0/24"
	vnetClientIP        = "10.66.0.2"
	vnetServerIP        = "10.66.0.3"
	iceDisconnected     = time.Second
	iceFailed           = 2 * time.Second
	iceKeepaliveInteval = 200 * time.Millisecond
)

// VirtualNetwork is an in-memory pion network with two hosts, so WebRTC peers
// connect without opening a socket. Client goes in the codexrtc.PeerConfig of
// the peer under test; the Backend's answer peer uses Server.
type VirtualNetwork struct {
	router   *vnet.Router
	stopOnce sync.Once
	stopErr  error
	Client   *webrtc.SettingEngine
	Server   *webrtc.SettingEngine
}

// NewVirtualNetwork starts the network. Close stops it.
func NewVirtualNetwork() (*VirtualNetwork, error) {
	router, err := vnet.NewRouter(&vnet.RouterConfig{CIDR: vnetCIDR, LoggerFactory: logging.NewDefaultLoggerFactory()})
	if err != nil {
		return nil, fmt.Errorf("fakecodex: virtual router: %w", err)
	}
	client, err := host(router, vnetClientIP)
	if err != nil {
		return nil, err
	}
	server, err := host(router, vnetServerIP)
	if err != nil {
		return nil, err
	}
	if err := router.Start(); err != nil {
		return nil, fmt.Errorf("fakecodex: start virtual router: %w", err)
	}
	return &VirtualNetwork{router: router, Client: client, Server: server}, nil
}

func host(router *vnet.Router, ip string) (*webrtc.SettingEngine, error) {
	network, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{ip}})
	if err != nil {
		return nil, fmt.Errorf("fakecodex: virtual host %s: %w", ip, err)
	}
	if err := router.AddNet(network); err != nil {
		return nil, fmt.Errorf("fakecodex: attach virtual host %s: %w", ip, err)
	}
	settings := &webrtc.SettingEngine{}
	settings.SetNet(network)
	settings.SetICETimeouts(iceDisconnected, iceFailed, iceKeepaliveInteval)
	return settings, nil
}

// Close stops the network. It is idempotent.
func (n *VirtualNetwork) Close() error {
	n.stopOnce.Do(func() { n.stopErr = n.router.Stop() })
	return n.stopErr
}
