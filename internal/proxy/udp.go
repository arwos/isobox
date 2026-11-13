package proxy

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// UDPAssociation carries IPv4 UDP datagrams through the relay selected by a
// SOCKS5 UDP ASSOCIATE request. Fragmentation is not implemented; fragmented
// datagrams are rejected instead of being partially relayed.
type UDPAssociation struct {
	packet  udpPacketConn
	control net.Conn
	relay   *net.UDPAddr
	done    chan struct{}
	once    sync.Once
	mu      sync.RWMutex
	err     error
	readMu  sync.Mutex
}

func newUDPAssociation(packet udpPacketConn, control net.Conn, relay *net.UDPAddr, ctx context.Context) *UDPAssociation {
	association := &UDPAssociation{
		packet:  packet,
		control: control,
		relay:   relay,
		done:    make(chan struct{}),
	}
	go association.watchControl()
	go association.watchContext(ctx)
	return association
}

// SendTo sends one unfragmented IPv4 or domain-name datagram through the
// SOCKS relay. The datagram itself is never sent to the destination directly.
func (association *UDPAssociation) SendTo(destination string, payload []byte) error {
	if association == nil || association.packet == nil || association.relay == nil {
		return fmt.Errorf("SOCKS5 UDP association is not configured")
	}
	if err := association.Err(); err != nil {
		return err
	}
	address, err := encodeAddress(destination)
	if err != nil {
		return err
	}
	packetLength := 3 + len(address) + len(payload)
	if packetLength > maxSOCKSUDPPayload {
		return fmt.Errorf("SOCKS5 UDP datagram exceeds the IPv4 packet limit")
	}
	datagram := make([]byte, 3, packetLength)
	// RSV is 0x0000 and FRAG is 0: fragmentation is not supported.
	datagram[0], datagram[1], datagram[2] = 0, 0, 0
	datagram = append(datagram, address...)
	datagram = append(datagram, payload...)
	written, err := association.packet.WriteToUDP(datagram, association.relay)
	if err != nil {
		if association.Err() != nil {
			return association.Err()
		}
		return fmt.Errorf("send SOCKS5 UDP relay datagram: %w", err)
	}
	if written != len(datagram) {
		return fmt.Errorf("send SOCKS5 UDP relay datagram: %w", io.ErrShortWrite)
	}
	return nil
}

// ReadFrom receives one valid relay datagram and copies its payload to buffer.
// Packets from other source endpoints are discarded. buffer should be large
// enough for one IPv4 UDP datagram (65507 bytes) to avoid truncation.
func (association *UDPAssociation) ReadFrom(buffer []byte) (int, string, error) {
	if association == nil || association.packet == nil || association.relay == nil {
		return 0, "", fmt.Errorf("SOCKS5 UDP association is not configured")
	}
	association.readMu.Lock()
	defer association.readMu.Unlock()
	for {
		length, source, err := association.packet.ReadFromUDP(buffer)
		if err != nil {
			if associationErr := association.Err(); associationErr != nil {
				return 0, "", associationErr
			}
			return 0, "", fmt.Errorf("read SOCKS5 UDP relay datagram: %w", err)
		}
		if !sameUDPAddress(source, association.relay) {
			continue
		}
		destination, payloadOffset, fragment, err := decodeUDPHeader(buffer[:length])
		if err != nil {
			return 0, "", err
		}
		if fragment != 0 {
			return 0, "", ErrUnsupportedFragment
		}
		payloadLength := length - payloadOffset
		copy(buffer, buffer[payloadOffset:length])
		return payloadLength, net.JoinHostPort(destination.Host, fmt.Sprintf("%d", destination.Port)), nil
	}
}

// SetReadDeadline controls how long ReadFrom can block. Closing or canceling
// the association also interrupts a blocked read.
func (association *UDPAssociation) SetReadDeadline(deadline time.Time) error {
	if association == nil || association.packet == nil {
		return net.ErrClosed
	}
	return association.packet.SetReadDeadline(deadline)
}

// Done is closed when the control connection fails, the association context
// is canceled, or Close is called.
func (association *UDPAssociation) Done() <-chan struct{} {
	if association == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return association.done
}

// Err reports why an association ended unexpectedly. Explicit Close has no
// error; callers can inspect Err after Done is closed.
func (association *UDPAssociation) Err() error {
	if association == nil {
		return net.ErrClosed
	}
	association.mu.RLock()
	defer association.mu.RUnlock()
	return association.err
}

// Close terminates the UDP socket and the SOCKS control connection. Closing
// the control connection also tells the SOCKS server to release its relay.
func (association *UDPAssociation) Close() error {
	if association == nil {
		return nil
	}
	association.shutdown(nil)
	return nil
}

func (association *UDPAssociation) watchControl() {
	var unexpected [1]byte
	n, err := association.control.Read(unexpected[:])
	if n > 0 {
		association.shutdown(fmt.Errorf("SOCKS5 UDP control connection received unexpected data"))
		return
	}
	if err != nil {
		association.shutdown(fmt.Errorf("SOCKS5 UDP control connection closed"))
		return
	}
	association.shutdown(fmt.Errorf("SOCKS5 UDP control connection ended"))
}

func (association *UDPAssociation) watchContext(ctx context.Context) {
	select {
	case <-ctx.Done():
		association.shutdown(ctx.Err())
	case <-association.done:
	}
}

func (association *UDPAssociation) shutdown(err error) {
	association.once.Do(func() {
		association.mu.Lock()
		association.err = err
		association.mu.Unlock()
		close(association.done)
		_ = association.packet.Close()
		_ = association.control.Close()
	})
}

func decodeUDPHeader(packet []byte) (socksAddress, int, byte, error) {
	if len(packet) < 4 {
		return socksAddress{}, 0, 0, fmt.Errorf("SOCKS5 UDP datagram is truncated")
	}
	if packet[0] != 0 || packet[1] != 0 {
		return socksAddress{}, 0, 0, fmt.Errorf("SOCKS5 UDP datagram has a nonzero reserved field")
	}
	fragment := packet[2]
	addressType := packet[3]
	index := 4
	var address socksAddress
	switch addressType {
	case socksAddressIPv4:
		if len(packet) < index+4+2 {
			return socksAddress{}, 0, 0, fmt.Errorf("SOCKS5 UDP IPv4 address is truncated")
		}
		address.Host = net.IP(packet[index : index+4]).String()
		index += 4
	case socksAddressDomain:
		if len(packet) < index+1 {
			return socksAddress{}, 0, 0, fmt.Errorf("SOCKS5 UDP domain length is truncated")
		}
		length := int(packet[index])
		index++
		if length == 0 || len(packet) < index+length+2 {
			return socksAddress{}, 0, 0, fmt.Errorf("SOCKS5 UDP domain address is truncated")
		}
		address.Host = string(packet[index : index+length])
		if !validHostname(address.Host) {
			return socksAddress{}, 0, 0, fmt.Errorf("SOCKS5 UDP relay returned an invalid domain address")
		}
		index += length
	case socksAddressIPv6:
		return socksAddress{}, 0, 0, ErrIPv6Address
	default:
		return socksAddress{}, 0, 0, fmt.Errorf("SOCKS5 UDP datagram uses an unsupported address type")
	}
	address.Port = binary.BigEndian.Uint16(packet[index : index+2])
	if address.Port == 0 {
		return socksAddress{}, 0, 0, fmt.Errorf("SOCKS5 UDP datagram has port zero")
	}
	index += 2
	return address, index, fragment, nil
}

func sameUDPAddress(left, right *net.UDPAddr) bool {
	return left != nil && right != nil && left.Port == right.Port && left.IP.To4() != nil && left.IP.To4().Equal(right.IP.To4())
}
