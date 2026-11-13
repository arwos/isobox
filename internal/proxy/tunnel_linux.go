//go:build linux

package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

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
	tunFile    *os.File
	stack      *stack.Stack
	link       *channel.Endpoint
	client     *socks5Client
	dnsAddress string
	ctx        context.Context
	cancel     context.CancelFunc
	deviceCtx  context.Context
	deviceEnd  context.CancelFunc
	flows      chan struct{}
	done       chan struct{}
	readDone   chan struct{}
	doneOnce   sync.Once
	closeOnce  sync.Once
	flowWG     sync.WaitGroup
	deviceWG   sync.WaitGroup
	flowMu     sync.Mutex
	closing    bool
	mu         sync.RWMutex
	err        error
}

// StartTUNTunnel takes ownership of tunFile on both success and failure.
// dnsAddress is the selected IPv4 resolver mapped from TransparentDNSAddress.
// It verifies TCP authentication and SOCKS5 UDP ASSOCIATE before returning,
// so the sandbox startup gate can stay closed when the proxy is unavailable.
func StartTUNTunnel(tunFile *os.File, alias, dnsAddress string) (*Tunnel, error) {
	if tunFile == nil {
		return nil, fmt.Errorf("proxy TUN descriptor is required")
	}
	parsedDNS := net.ParseIP(dnsAddress)
	if parsedDNS == nil || parsedDNS.To4() == nil {
		_ = tunFile.Close()
		return nil, fmt.Errorf("proxy DNS destination must be an IPv4 address")
	}
	config, err := loadRuntimeConfig(alias)
	if err != nil {
		_ = tunFile.Close()
		return nil, err
	}
	client, err := newSOCKS5Client(config)
	if err != nil {
		_ = tunFile.Close()
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	deviceCtx, deviceEnd := context.WithCancel(context.Background())
	tunnel := &Tunnel{
		tunFile:    tunFile,
		client:     client,
		dnsAddress: parsedDNS.To4().String(),
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
		return nil, err
	}
	if err := tunnel.probeProxy(); err != nil {
		_ = tunnel.Close()
		return nil, err
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
	destination, err := tunnel.destination(connectionID.LocalAddress, connectionID.LocalPort)
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
		ctx, cancel := context.WithTimeout(tunnel.ctx, tunnelConnectTimeout)
		remote, err := tunnel.client.DialContext(ctx, "tcp4", destination)
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
	destination, err := tunnel.destination(connectionID.LocalAddress, connectionID.LocalPort)
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
		tunnel.bridgeUDP(local, destination)
	}()
	return true
}

func (tunnel *Tunnel) bridgeTCP(local, remote net.Conn) {
	defer local.Close()
	defer remote.Close()
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
	defer local.Close()
	association, err := tunnel.client.UDPAssociate(ctx)
	if err != nil {
		return
	}
	defer association.Close()

	closeLocalOnStop := make(chan struct{})
	go func() {
		select {
		case <-association.Done():
			_ = local.Close()
		case <-ctx.Done():
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

func ipv4Destination(address tcpip.Address, port uint16) (string, error) {
	if address.Len() != header.IPv4AddressSize || port == 0 {
		return "", fmt.Errorf("TUN destination must be IPv4 with a nonzero port")
	}
	host := net.IP(address.AsSlice()).String()
	if ip := net.ParseIP(host); ip == nil || ip.To4() == nil {
		return "", fmt.Errorf("TUN destination is not IPv4")
	}
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

func (tunnel *Tunnel) destination(address tcpip.Address, port uint16) (string, error) {
	if tunnel != nil && address.String() == TransparentDNSAddress && port == 53 {
		if ip := net.ParseIP(tunnel.dnsAddress); ip == nil || ip.To4() == nil {
			return "", fmt.Errorf("proxy DNS destination is not IPv4")
		}
		return net.JoinHostPort(tunnel.dnsAddress, "53"), nil
	}
	return ipv4Destination(address, port)
}
