//go:build linux

package proxy

import (
	"context"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

func TestIPv4Destination(t *testing.T) {
	tests := []struct {
		name    string
		address tcpip.Address
		port    uint16
		want    string
		wantErr bool
	}{
		{name: "IPv4", address: tcpip.AddrFrom4([4]byte{192, 0, 2, 8}), port: 443, want: "192.0.2.8:443"},
		{name: "IPv6", address: tcpip.AddrFrom16([16]byte{0x20, 0x01, 0x0d, 0xb8}), port: 443, wantErr: true},
		{name: "zero port", address: tcpip.AddrFrom4([4]byte{192, 0, 2, 8}), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ipv4Destination(test.address, test.port)
			if (err != nil) != test.wantErr {
				t.Fatalf("ipv4Destination() error = %v, wantErr %t", err, test.wantErr)
			}
			if err == nil && got != test.want {
				t.Fatalf("ipv4Destination() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTunnelRedirectsSyntheticDNSAddressToConfiguredResolver(t *testing.T) {
	tunnel := &Tunnel{dnsAddress: "127.0.0.53"}
	tests := []struct {
		name    string
		address tcpip.Address
		port    uint16
		want    string
	}{
		{name: "UDP DNS", address: tcpip.AddrFrom4([4]byte{10, 89, 0, 2}), port: 53, want: "127.0.0.53:53"},
		{name: "TCP DNS", address: tcpip.AddrFrom4([4]byte{10, 89, 0, 2}), port: 53, want: "127.0.0.53:53"},
		{name: "other port", address: tcpip.AddrFrom4([4]byte{10, 89, 0, 2}), port: 443, want: "10.89.0.2:443"},
		{name: "other address", address: tcpip.AddrFrom4([4]byte{192, 0, 2, 53}), port: 53, want: "192.0.2.53:53"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := tunnel.destination(test.address, test.port)
			if err != nil || got != test.want {
				t.Fatalf("destination() = (%q, %v), want (%q, nil)", got, err, test.want)
			}
		})
	}
}

func TestProxyTUNPacketFilter(t *testing.T) {
	tests := []struct {
		name    string
		packet  []byte
		allowed bool
	}{
		{name: "IPv4 TCP", packet: ipv4Packet(byte(tcp.ProtocolNumber)), allowed: true},
		{name: "IPv4 UDP", packet: ipv4Packet(byte(udp.ProtocolNumber)), allowed: true},
		{name: "IPv4 ICMP", packet: ipv4Packet(1)},
		{name: "IPv4 raw protocol", packet: ipv4Packet(47)},
		{name: "IPv6", packet: []byte{0x60, 0, 0, 0, 0, 0, 6, 64}},
		{name: "short IPv4", packet: []byte{0x45, 0, 0}},
		{name: "empty"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isProxyTUNPacket(test.packet); got != test.allowed {
				t.Fatalf("isProxyTUNPacket() = %v, want %v", got, test.allowed)
			}
		})
	}
}

func ipv4Packet(protocol byte) []byte {
	packet := make([]byte, header.IPv4MinimumSize)
	packet[0] = 0x45
	packet[9] = protocol
	return packet
}

func TestTunnelBridgeTCPCopiesBothDirections(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tunnel := &Tunnel{ctx: ctx, cancel: cancel}
	application, local := net.Pipe()
	remote, proxyServer := net.Pipe()
	done := make(chan struct{})
	go func() {
		tunnel.bridgeTCP(local, remote)
		close(done)
	}()

	if _, err := application.Write([]byte("request")); err != nil {
		t.Fatal(err)
	}
	request := make([]byte, len("request"))
	if _, err := io.ReadFull(proxyServer, request); err != nil {
		t.Fatal(err)
	}
	if string(request) != "request" {
		t.Fatalf("proxy received %q, want request", request)
	}
	if _, err := proxyServer.Write([]byte("response")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len("response"))
	if _, err := io.ReadFull(application, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != "response" {
		t.Fatalf("application received %q, want response", response)
	}
	_ = application.Close()
	_ = proxyServer.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("TCP flow bridge did not stop after both peers closed")
	}
}

func TestTunnelCloseStopsPacketWorkers(t *testing.T) {
	device, peer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	deviceCtx, deviceEnd := context.WithCancel(context.Background())
	tunnel := &Tunnel{
		tunFile:   device,
		ctx:       ctx,
		cancel:    cancel,
		deviceCtx: deviceCtx,
		deviceEnd: deviceEnd,
		flows:     make(chan struct{}, tunnelMaxFlows),
		done:      make(chan struct{}),
		readDone:  make(chan struct{}),
	}
	if err := tunnel.openStack(); err != nil {
		_ = device.Close()
		t.Fatal(err)
	}
	if !tunnel.reserveFlow() {
		t.Fatal("failed to reserve a test flow")
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		tunnel.releaseFlow()
	}()
	closed := make(chan struct{})
	go func() {
		_ = tunnel.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("tunnel closed before its active flow stopped")
	case <-time.After(30 * time.Millisecond):
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("TUN packet workers did not stop")
	}
}

func TestReserveFlowRejectsAfterTunnelClosing(t *testing.T) {
	tunnel := &Tunnel{flows: make(chan struct{}, 1)}
	tunnel.flowMu.Lock()
	tunnel.closing = true
	tunnel.flowMu.Unlock()
	if tunnel.reserveFlow() {
		t.Fatal("reserveFlow() succeeded after tunnel shutdown began")
	}
}
