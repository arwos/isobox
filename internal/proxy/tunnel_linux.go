//go:build linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/arwos/isobox/internal/profile"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const (
	tunnelMTU              = 1500
	tunnelMaxFlows         = 512
	tunnelMaxQueuedPackets = 1024
	tunnelConnectTimeout   = 12 * time.Second
	tunnelUDPIdleTimeout   = 90 * time.Second
	tunnelProbeTimeout     = 12 * time.Second
	tunnelUDPBufferLength  = 65507
	// TransparentDNSAddress is routed into the TUN and mapped to dnsAddress.
	TransparentDNSAddress = "10.89.0.2"
)

// Tunnel owns the IPv4-only user-space stack attached to tunFile. Credentials
// remain inside the proxy package; callers receive only lifecycle methods.
type Tunnel struct {
	tunFile      *os.File
	stack        *stack.Stack
	link         *channel.Endpoint
	mode         profile.NetworkMode
	client       *socks5Client
	dnsAddress   string
	dnsAddresses []net.IP
	allowRules   []compiledAllowRule
	enforceACL   bool
	dnsRecords   map[string][]net.IP
	eventLog     *egressLogger
	ctx          context.Context
	cancel       context.CancelFunc
	deviceCtx    context.Context
	deviceEnd    context.CancelFunc
	flows        chan struct{}
	done         chan struct{}
	readDone     chan struct{}
	doneOnce     sync.Once
	closeOnce    sync.Once
	flowWG       sync.WaitGroup
	deviceWG     sync.WaitGroup
	flowMu       sync.Mutex
	closing      bool
	mu           sync.RWMutex
	err          error
}

// StartTUNTunnel takes ownership of tunFile on both success and failure.
// dnsAddress is the selected IPv4 resolver mapped from TransparentDNSAddress.
// It verifies TCP authentication and SOCKS5 UDP ASSOCIATE before returning,
// so the sandbox startup gate can stay closed when the proxy is unavailable.
func StartTUNTunnel(tunFile *os.File, alias, dnsAddress string) (*Tunnel, error) {
	return StartTUNTunnelWithPolicy(tunFile, TUNPolicy{
		Mode:       profile.NetworkModeProxy,
		ProxyAlias: alias,
		DNSAddress: dnsAddress,
	})
}

// StartTUNTunnelWithPolicy creates the IPv4-only transparent network path for
// external SOCKS5 or built-in egress mode. It takes ownership of tunFile on
// both success and failure.
func StartTUNTunnelWithPolicy(tunFile *os.File, policy TUNPolicy) (*Tunnel, error) {
	if tunFile == nil {
		return nil, fmt.Errorf("proxy TUN descriptor is required")
	}
	if policy.Mode != profile.NetworkModeProxy && policy.Mode != profile.NetworkModeEgress {
		_ = tunFile.Close()
		return nil, fmt.Errorf("TUN policy does not support network mode %q", policy.Mode)
	}
	if policy.Mode == profile.NetworkModeProxy && policy.ProxyAlias == "" {
		_ = tunFile.Close()
		return nil, fmt.Errorf("proxy network mode requires a SOCKS5 alias")
	}
	if policy.Mode == profile.NetworkModeEgress && policy.ProxyAlias != "" {
		_ = tunFile.Close()
		return nil, fmt.Errorf("egress network mode cannot use a SOCKS5 alias")
	}
	dnsAddressValues := append([]string(nil), policy.DNSAddresses...)
	if len(dnsAddressValues) == 0 && policy.DNSAddress != "" {
		dnsAddressValues = []string{policy.DNSAddress}
	}
	parsedDNSAddresses := make([]net.IP, 0, len(dnsAddressValues))
	for _, value := range dnsAddressValues {
		parsedDNS := net.ParseIP(value)
		if parsedDNS == nil || parsedDNS.To4() == nil {
			_ = tunFile.Close()
			return nil, fmt.Errorf("proxy DNS destination must be an IPv4 address")
		}
		dnsAddress := parsedDNS.To4().String()
		if dnsAddress == TransparentDNSAddress {
			_ = tunFile.Close()
			return nil, fmt.Errorf("upstream DNS cannot point to the internal resolver endpoint")
		}
		parsedDNSAddresses = append(parsedDNSAddresses, parsedDNS.To4())
	}
	if policy.Mode == profile.NetworkModeProxy && len(parsedDNSAddresses) == 0 {
		_ = tunFile.Close()
		return nil, fmt.Errorf("proxy DNS destination must be an IPv4 address")
	}
	allowRules, err := compileAllowRules(policy.Allow)
	if err != nil {
		_ = tunFile.Close()
		return nil, err
	}
	dnsRecords, err := compileDNSRecords(policy.Records)
	if err != nil {
		_ = tunFile.Close()
		return nil, err
	}
	var eventLog *egressLogger
	if policy.Mode == profile.NetworkModeEgress {
		eventLog, err = openEgressLogger(policy.AppID)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "isobox: egress event logging unavailable: %v; provide a writable user-owned log file under /var/log to enable it\n", err)
		}
	}
	var client *socks5Client
	if policy.Mode == profile.NetworkModeProxy {
		config, err := loadRuntimeConfig(policy.ProxyAlias)
		if err != nil {
			_ = tunFile.Close()
			return nil, err
		}
		client, err = newSOCKS5Client(config)
		if err != nil {
			_ = tunFile.Close()
			return nil, err
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	deviceCtx, deviceEnd := context.WithCancel(context.Background())
	tunnel := &Tunnel{
		tunFile:      tunFile,
		mode:         policy.Mode,
		client:       client,
		dnsAddresses: parsedDNSAddresses,
		dnsAddress: func() string {
			if len(parsedDNSAddresses) != 0 {
				return parsedDNSAddresses[0].String()
			}
			return ""
		}(),
		allowRules: allowRules,
		enforceACL: policy.Mode == profile.NetworkModeEgress || len(allowRules) != 0,
		dnsRecords: dnsRecords,
		eventLog:   eventLog,
		ctx:        ctx,
		cancel:     cancel,
		deviceCtx:  deviceCtx,
		deviceEnd:  deviceEnd,
		flows:      make(chan struct{}, tunnelMaxFlows),
		done:       make(chan struct{}),
		readDone:   make(chan struct{}),
	}
	if err := tunnel.openStack(); err != nil {
		cancel()
		deviceEnd()
		_ = tunFile.Close()
		_ = eventLog.close()
		return nil, err
	}
	if tunnel.client != nil {
		if err := tunnel.probeProxy(); err != nil {
			_ = tunnel.Close()
			return nil, err
		}
	}
	if err := tunnel.Err(); err != nil {
		_ = tunnel.Close()
		return nil, fmt.Errorf("proxy TUN stopped during startup: %w", err)
	}
	return tunnel, nil
}

// Done closes after a fatal TUN or stack error, or after Close.
func (tunnel *Tunnel) Done() <-chan struct{} {
	if tunnel == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return tunnel.done
}

// Err reports a fatal data-plane error. Per-destination SOCKS failures close
// only that flow; they never cause direct network fallback.
func (tunnel *Tunnel) Err() error {
	if tunnel == nil {
		return net.ErrClosed
	}
	tunnel.mu.RLock()
	defer tunnel.mu.RUnlock()
	return tunnel.err
}

// Close stops the stack, all active flows, and the transferred TUN descriptor.
func (tunnel *Tunnel) Close() error {
	if tunnel == nil {
		return nil
	}
	var closeErr error
	tunnel.closeOnce.Do(func() {
		tunnel.flowMu.Lock()
		tunnel.closing = true
		tunnel.flowMu.Unlock()
		tunnel.cancel()
		if tunnel.stack != nil {
			if tunnel.readDone != nil {
				<-tunnel.readDone
			}
			tunnel.stack.Destroy()
		}
		tunnel.deviceEnd()
		tunnel.deviceWG.Wait()
		if tunnel.link != nil {
			tunnel.link.Drain()
		}
		if tunnel.tunFile != nil {
			closeErr = tunnel.tunFile.Close()
		}
		tunnel.flowWG.Wait()
		if err := tunnel.eventLog.close(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "isobox: close egress event log: %v\n", err)
		}
		tunnel.doneOnce.Do(func() { close(tunnel.done) })
	})
	return closeErr
}

func (tunnel *Tunnel) openStack() error {
	link := channel.New(tunnelMaxQueuedPackets, tunnelMTU, "")
	if link == nil {
		return fmt.Errorf("create TUN packet queue")
	}

	stackInstance := stack.New(stack.Options{
		NetworkProtocols: []stack.NetworkProtocolFactory{
			ipv4.NewProtocol,
		},
		TransportProtocols: []stack.TransportProtocolFactory{
			tcp.NewProtocol,
			udp.NewProtocol,
		},
	})
	tunnel.stack = stackInstance
	const nicID tcpip.NICID = 1
	if err := stackInstance.CreateNIC(nicID, link); err != nil {
		stackInstance.Destroy()
		return fmt.Errorf("attach TUN packet endpoint to IPv4 stack: %s", err.String())
	}
	if err := stackInstance.SetPromiscuousMode(nicID, true); err != nil {
		stackInstance.Destroy()
		return fmt.Errorf("enable IPv4 TUN packet reception: %s", err.String())
	}
	if err := stackInstance.SetSpoofing(nicID, true); err != nil {
		stackInstance.Destroy()
		return fmt.Errorf("enable IPv4 TUN packet replies: %s", err.String())
	}
	defaultRoute, err := tcpip.NewSubnet(
		tcpip.AddrFrom4([4]byte{}),
		tcpip.MaskFrom("\x00\x00\x00\x00"),
	)
	if err != nil {
		stackInstance.Destroy()
		return fmt.Errorf("create IPv4 default route for TUN stack: %w", err)
	}
	stackInstance.SetRouteTable([]tcpip.Route{{Destination: defaultRoute, NIC: nicID}})

	tcpForwarder := tcp.NewForwarder(stackInstance, 0, tunnelMaxFlows, tunnel.handleTCP)
	udpForwarder := udp.NewForwarder(stackInstance, tunnel.handleUDP)
	stackInstance.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpForwarder.HandlePacket)
	stackInstance.SetTransportProtocolHandler(udp.ProtocolNumber, udpForwarder.HandlePacket)
	tunnel.link = link
	tunnel.deviceWG.Add(2)
	go tunnel.readTUNPackets()
	go tunnel.writeTUNPackets()
	return nil
}

func (tunnel *Tunnel) probeProxy() error {
	ctx, cancel := context.WithTimeout(tunnel.ctx, tunnelProbeTimeout)
	defer cancel()
	if err := tunnel.client.probe(ctx); err != nil {
		return fmt.Errorf("SOCKS5 TCP readiness check failed: %w", err)
	}
	association, err := tunnel.client.UDPAssociate(ctx)
	if err != nil {
		return fmt.Errorf("SOCKS5 UDP readiness check failed: %w", err)
	}
	_ = association.Close()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("SOCKS5 readiness check was canceled: %w", err)
	}
	return nil
}

func (tunnel *Tunnel) handleTCP(request *tcp.ForwarderRequest) {
	if !tunnel.reserveFlow() {
		request.Complete(true)
		return
	}
	connectionID := request.ID()
	isDNS := net.IP(connectionID.LocalAddress.AsSlice()).To4().Equal(net.ParseIP(TransparentDNSAddress).To4()) && connectionID.LocalPort == 53
	var destination string
	var err error
	if !isDNS {
		destination, err = tunnel.destination(net.IP(connectionID.LocalAddress.AsSlice()), connectionID.LocalPort, profile.PortProtocolTCP)
	}
	if err != nil {
		tunnel.releaseFlow()
		request.Complete(true)
		return
	}
	var queue waiter.Queue
	endpoint, tcpipErr := request.CreateEndpoint(&queue)
	if tcpipErr != nil {
		tunnel.releaseFlow()
		request.Complete(true)
		return
	}
	request.Complete(false)
	local := gonet.NewTCPConn(&queue, endpoint)
	go func() {
		defer tunnel.releaseFlow()
		if isDNS {
			tunnel.serveDNSTCP(local)
			return
		}
		ctx, cancel := context.WithTimeout(tunnel.ctx, tunnelConnectTimeout)
		remote, err := tunnel.dialContext(ctx, "tcp4", destination)
		cancel()
		if err != nil {
			_ = local.Close()
			return
		}
		tunnel.bridgeTCP(local, remote)
	}()
}

func (tunnel *Tunnel) handleUDP(request *udp.ForwarderRequest) bool {
	if !tunnel.reserveFlow() {
		return false
	}
	connectionID := request.ID()
	isDNS := net.IP(connectionID.LocalAddress.AsSlice()).To4().Equal(net.ParseIP(TransparentDNSAddress).To4()) && connectionID.LocalPort == 53
	var destination string
	var err error
	if !isDNS {
		destination, err = tunnel.destination(net.IP(connectionID.LocalAddress.AsSlice()), connectionID.LocalPort, profile.PortProtocolUDP)
	}
	if err != nil {
		tunnel.releaseFlow()
		return false
	}
	var queue waiter.Queue
	endpoint, tcpipErr := request.CreateEndpoint(&queue)
	if tcpipErr != nil {
		tunnel.releaseFlow()
		return false
	}
	local := gonet.NewUDPConn(&queue, endpoint)
	go func() {
		defer tunnel.releaseFlow()
		if isDNS {
			tunnel.serveDNSUDP(local)
			return
		}
		tunnel.bridgeUDP(local, destination)
	}()
	return true
}

func (tunnel *Tunnel) bridgeTCP(local, remote net.Conn) {
	defer func() { _ = local.Close() }()
	defer func() { _ = remote.Close() }()
	stopClose := context.AfterFunc(tunnel.ctx, func() {
		_ = local.Close()
		_ = remote.Close()
	})
	defer stopClose()

	results := make(chan error, 2)
	copyHalf := func(destination, source net.Conn) {
		_, err := io.Copy(destination, source)
		if closer, ok := destination.(interface{ CloseWrite() error }); ok {
			_ = closer.CloseWrite()
		}
		results <- err
	}
	go copyHalf(remote, local)
	go copyHalf(local, remote)
	first := <-results
	if first != nil && !errors.Is(first, net.ErrClosed) {
		_ = local.Close()
		_ = remote.Close()
	}
	<-results
}

func (tunnel *Tunnel) bridgeUDP(local *gonet.UDPConn, destination string) {
	ctx, cancel := context.WithCancel(tunnel.ctx)
	defer cancel()
	defer func() { _ = local.Close() }()
	if tunnel.client == nil {
		remote, err := tunnel.dialContext(ctx, "udp4", destination)
		if err != nil {
			return
		}
		defer func() { _ = remote.Close() }()
		closeLocalOnStop := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				_ = local.Close()
			case <-closeLocalOnStop:
			}
		}()
		results := make(chan error, 2)
		go func() {
			buffer := make([]byte, tunnelUDPBufferLength)
			for {
				n, err := local.Read(buffer)
				if err != nil {
					results <- err
					return
				}
				_ = remote.SetDeadline(time.Now().Add(tunnelUDPIdleTimeout))
				if _, err := remote.Write(buffer[:n]); err != nil {
					results <- err
					return
				}
			}
		}()
		go func() {
			buffer := make([]byte, tunnelUDPBufferLength)
			for {
				_ = remote.SetDeadline(time.Now().Add(tunnelUDPIdleTimeout))
				n, err := remote.Read(buffer)
				if err != nil {
					results <- err
					return
				}
				if _, err := local.Write(buffer[:n]); err != nil {
					results <- err
					return
				}
			}
		}()
		<-results
		cancel()
		_ = local.Close()
		_ = remote.Close()
		<-results
		close(closeLocalOnStop)
		return
	}
	association, err := tunnel.client.UDPAssociate(ctx)
	if err != nil {
		return
	}
	defer func() { _ = association.Close() }()

	closeLocalOnStop := make(chan struct{})
	go func() {
		select {
		case <-association.Done():
			_ = local.Close()
		case <-ctx.Done():
			_ = local.Close()
		}
		close(closeLocalOnStop)
	}()

	activity := func() {
		deadline := time.Now().Add(tunnelUDPIdleTimeout)
		_ = local.SetReadDeadline(deadline)
		_ = association.SetReadDeadline(deadline)
	}
	activity()
	results := make(chan error, 2)
	go func() {
		buffer := make([]byte, tunnelUDPBufferLength)
		for {
			n, err := local.Read(buffer)
			if err != nil {
				results <- err
				return
			}
			activity()
			if err := association.SendTo(destination, buffer[:n]); err != nil {
				results <- err
				return
			}
		}
	}()
	go func() {
		buffer := make([]byte, tunnelUDPBufferLength)
		for {
			n, source, err := association.ReadFrom(buffer)
			if err != nil {
				results <- err
				return
			}
			if source != destination {
				continue
			}
			activity()
			if _, err := local.Write(buffer[:n]); err != nil {
				results <- err
				return
			}
		}
	}()
	<-results
	cancel()
	_ = local.Close()
	_ = association.Close()
	<-results
	<-closeLocalOnStop
}

func (tunnel *Tunnel) reserveFlow() bool {
	tunnel.flowMu.Lock()
	defer tunnel.flowMu.Unlock()
	if tunnel.closing {
		return false
	}
	select {
	case tunnel.flows <- struct{}{}:
		tunnel.flowWG.Add(1)
		return true
	default:
		return false
	}
}

func (tunnel *Tunnel) releaseFlow() {
	<-tunnel.flows
	tunnel.flowWG.Done()
}

func (tunnel *Tunnel) readTUNPackets() {
	defer tunnel.deviceWG.Done()
	if tunnel.tunFile == nil || tunnel.link == nil {
		tunnel.fail(fmt.Errorf("TUN packet reader is not configured"))
		close(tunnel.readDone)
		return
	}
	defer close(tunnel.readDone)
	fd := int(tunnel.tunFile.Fd())
	descriptor := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	packet := make([]byte, tunnelMTU+1)
	for {
		if tunnel.ctx.Err() != nil {
			return
		}
		_, err := unix.Poll(descriptor, 100)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if tunnel.ctx.Err() == nil {
				tunnel.fail(fmt.Errorf("wait for IPv4 TUN packets: %w", err))
			}
			return
		}
		if descriptor[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			if tunnel.ctx.Err() == nil {
				tunnel.fail(fmt.Errorf("IPv4 TUN packet device reported poll events %#x", descriptor[0].Revents))
			}
			return
		}
		if descriptor[0].Revents&unix.POLLIN == 0 {
			continue
		}
		n, err := unix.Read(fd, packet)
		if err != nil {
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
				continue
			}
			if tunnel.ctx.Err() == nil {
				tunnel.fail(fmt.Errorf("read IPv4 TUN packet: %w", err))
			}
			return
		}
		if n == 0 || !isProxyTUNPacket(packet[:n]) {
			continue
		}
		inbound := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(packet[:n])})
		tunnel.link.InjectInbound(ipv4.ProtocolNumber, inbound)
		inbound.DecRef()
	}
}

func (tunnel *Tunnel) writeTUNPackets() {
	defer tunnel.deviceWG.Done()
	if tunnel.tunFile == nil || tunnel.link == nil {
		tunnel.fail(fmt.Errorf("TUN packet writer is not configured"))
		return
	}
	fd := int(tunnel.tunFile.Fd())
	for {
		packet := tunnel.link.ReadContext(tunnel.deviceCtx)
		if packet == nil {
			return
		}
		view := packet.ToView()
		err := tunnel.writeTUNPacket(fd, view.AsSlice())
		view.Release()
		packet.DecRef()
		if err != nil {
			if tunnel.ctx.Err() == nil {
				tunnel.fail(fmt.Errorf("write IPv4 TUN packet: %w", err))
			}
			return
		}
	}
}

func (tunnel *Tunnel) writeTUNPacket(fd int, packet []byte) error {
	descriptor := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
	for {
		if tunnel.deviceCtx.Err() != nil {
			return context.Canceled
		}
		n, err := unix.Write(fd, packet)
		if err == nil {
			if n != len(packet) {
				return io.ErrShortWrite
			}
			return nil
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
			return err
		}
		_, err = unix.Poll(descriptor, 100)
		if err != nil && !errors.Is(err, unix.EINTR) {
			return err
		}
		if descriptor[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return fmt.Errorf("TUN packet device reported poll events %#x", descriptor[0].Revents)
		}
	}
}

func isProxyTUNPacket(packet []byte) bool {
	if len(packet) < header.IPv4MinimumSize || len(packet) > tunnelMTU || packet[0]>>4 != 4 {
		return false
	}
	protocol := header.IPv4(packet).Protocol()
	return protocol == byte(tcp.ProtocolNumber) || protocol == byte(udp.ProtocolNumber)
}

func (tunnel *Tunnel) fail(err error) {
	if err == nil {
		return
	}
	tunnel.mu.Lock()
	if tunnel.err == nil {
		tunnel.err = err
	}
	tunnel.mu.Unlock()
	tunnel.doneOnce.Do(func() { close(tunnel.done) })
}
