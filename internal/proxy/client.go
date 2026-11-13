package proxy

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	socksVersion        = 5
	socksAuthVersion    = 1
	socksMethodNone     = 0
	socksMethodUser     = 2
	socksMethodReject   = 0xff
	socksCommandConnect = 1
	socksCommandUDP     = 3
	socksAddressIPv4    = 1
	socksAddressDomain  = 3
	socksAddressIPv6    = 4
	defaultSOCKSTimeout = 10 * time.Second
	maxSOCKSUDPPayload  = 65507
)

var (
	// ErrIPv6Address indicates that a SOCKS address used IPv6, which is not
	// permitted by isobox's IPv4-only network contract.
	ErrIPv6Address = errors.New("SOCKS5 IPv6 addresses are disabled")
	// ErrUnsupportedFragment indicates that a SOCKS UDP packet used FRAG != 0.
	ErrUnsupportedFragment = errors.New("SOCKS5 UDP fragmentation is unsupported")
)

// ReplyError is returned when a SOCKS5 server rejects a CONNECT or UDP
// ASSOCIATE request. Code is the RFC 1928 REP value.
type ReplyError struct {
	Code byte
}

func (err *ReplyError) Error() string {
	return fmt.Sprintf("SOCKS5 server rejected request (reply 0x%02x)", err.Code)
}

type contextDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

type udpPacketConn interface {
	Close() error
	ReadFromUDP([]byte) (int, *net.UDPAddr, error)
	SetReadDeadline(time.Time) error
	WriteToUDP([]byte, *net.UDPAddr) (int, error)
}

type socks5Client struct {
	server    string
	auth      AuthMode
	username  string
	password  string
	dialer    contextDialer
	timeout   time.Duration
	listenUDP func() (udpPacketConn, error)
}

type socksAddress struct {
	Host string
	Port uint16
}

// newSOCKS5Client builds a SOCKS5 client from credentials loaded inside the
// proxy package. The CLI receives no reference to this client or its secrets.
func newSOCKS5Client(config runtimeConfig) (*socks5Client, error) {
	server, err := normalizeServer(config.server)
	if err != nil {
		return nil, fmt.Errorf("stored SOCKS5 server is invalid")
	}
	if err := validateCredentials(config.auth, config.username, config.password); err != nil {
		return nil, fmt.Errorf("stored SOCKS5 authentication is invalid")
	}
	return &socks5Client{
		server:   server,
		auth:     config.auth,
		username: config.username,
		password: config.password,
		dialer:   &net.Dialer{},
		timeout:  defaultSOCKSTimeout,
	}, nil
}

// DialContext opens an IPv4 TCP connection through SOCKS5 CONNECT. It never
// retries the destination directly if dialing or any SOCKS exchange fails.
func (client *socks5Client) DialContext(ctx context.Context, network, destination string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" {
		return nil, fmt.Errorf("SOCKS5 CONNECT supports TCP over IPv4 only")
	}
	encoded, err := encodeAddress(destination)
	if err != nil {
		return nil, err
	}
	connection, _, err := client.command(ctx, socksCommandConnect, encoded)
	if err != nil {
		return nil, err
	}
	return connection, nil
}

// UDPAssociate opens one SOCKS5 UDP association. The returned association is
// tied to ctx and closes when ctx is canceled, the control connection ends,
// or Close is called.
func (client *socks5Client) UDPAssociate(ctx context.Context) (*UDPAssociation, error) {
	// RFC 1928 requires an all-zero address when the client's UDP endpoint is
	// not known before the association is created.
	zeroAddress := []byte{socksAddressIPv4, 0, 0, 0, 0, 0, 0}
	control, bound, err := client.command(ctx, socksCommandUDP, zeroAddress)
	if err != nil {
		return nil, err
	}
	relay, err := resolveUDPRelay(ctx, control, bound)
	if err != nil {
		_ = control.Close()
		return nil, err
	}
	listenUDP := client.listenUDP
	if listenUDP == nil {
		listenUDP = func() (udpPacketConn, error) {
			return net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
		}
	}
	packet, err := listenUDP()
	if err != nil {
		_ = control.Close()
		return nil, fmt.Errorf("open IPv4 UDP socket for SOCKS5 relay: %w", err)
	}
	if err := ctx.Err(); err != nil {
		_ = packet.Close()
		_ = control.Close()
		return nil, err
	}
	association := newUDPAssociation(packet, control, relay, ctx)
	return association, nil
}

// probe verifies that the configured server accepts an IPv4 TCP connection and
// the selected SOCKS5 authentication method without opening a destination.
func (client *socks5Client) probe(ctx context.Context) error {
	connection, stopContextClose, err := client.connectControl(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Time{}); err != nil {
		stopContextClose()
		return fmt.Errorf("clear SOCKS5 probe deadline: %w", err)
	}
	if !stopContextClose() {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		return context.Canceled
	}
	return nil
}

func (client *socks5Client) command(ctx context.Context, command byte, encodedAddress []byte) (net.Conn, socksAddress, error) {
	connection, stopContextClose, err := client.connectControl(ctx)
	if err != nil {
		return nil, socksAddress{}, err
	}
	keepConnection := false
	defer func() {
		if !keepConnection {
			stopContextClose()
			_ = connection.Close()
		}
	}()
	request := make([]byte, 3, 3+len(encodedAddress))
	request[0], request[1], request[2] = socksVersion, command, 0
	request = append(request, encodedAddress...)
	if err := writeFull(connection, request); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, socksAddress{}, contextErr
		}
		return nil, socksAddress{}, fmt.Errorf("write SOCKS5 request: %w", err)
	}
	replyCode, bound, err := readReply(connection)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, socksAddress{}, contextErr
		}
		return nil, socksAddress{}, err
	}
	if replyCode != 0 {
		return nil, socksAddress{}, &ReplyError{Code: replyCode}
	}
	if err := connection.SetDeadline(time.Time{}); err != nil {
		return nil, socksAddress{}, fmt.Errorf("clear SOCKS5 handshake deadline: %w", err)
	}
	if !stopContextClose() {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, socksAddress{}, contextErr
		}
		return nil, socksAddress{}, context.Canceled
	}
	keepConnection = true
	return connection, bound, nil
}

func (client *socks5Client) connectControl(ctx context.Context) (net.Conn, func() bool, error) {
	if ctx == nil {
		return nil, nil, fmt.Errorf("SOCKS5 context is required")
	}
	if client == nil || client.dialer == nil || client.server == "" {
		return nil, nil, fmt.Errorf("SOCKS5 client is not configured")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	connection, err := client.dialer.DialContext(ctx, "tcp4", client.server)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, nil, contextErr
		}
		return nil, nil, fmt.Errorf("connect to SOCKS5 server: %w", err)
	}
	stopContextClose := context.AfterFunc(ctx, func() { _ = connection.Close() })
	keepConnection := false
	defer func() {
		if !keepConnection {
			stopContextClose()
			_ = connection.Close()
		}
	}()
	deadline := time.Now().Add(client.timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return nil, nil, fmt.Errorf("set SOCKS5 handshake deadline: %w", err)
	}
	if err := client.negotiate(connection); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, nil, contextErr
		}
		return nil, nil, err
	}
	keepConnection = true
	return connection, stopContextClose, nil
}

func (client *socks5Client) negotiate(connection net.Conn) error {
	method := byte(socksMethodNone)
	if client.auth == AuthUserPass {
		method = socksMethodUser
	}
	if client.auth != AuthNone && client.auth != AuthUserPass {
		return fmt.Errorf("unsupported SOCKS5 authentication method")
	}
	if err := writeFull(connection, []byte{socksVersion, 1, method}); err != nil {
		return fmt.Errorf("write SOCKS5 method negotiation: %w", err)
	}
	var selection [2]byte
	if _, err := io.ReadFull(connection, selection[:]); err != nil {
		return fmt.Errorf("read SOCKS5 method selection: %w", err)
	}
	if selection[0] != socksVersion {
		return fmt.Errorf("SOCKS5 server returned an invalid method-selection version")
	}
	if selection[1] == socksMethodReject {
		return fmt.Errorf("SOCKS5 server rejected all offered authentication methods")
	}
	if selection[1] != method {
		return fmt.Errorf("SOCKS5 server selected an authentication method that was not offered")
	}
	if method == socksMethodNone {
		return nil
	}
	username, password := []byte(client.username), []byte(client.password)
	message := make([]byte, 0, 3+len(username)+len(password))
	message = append(message, socksAuthVersion, byte(len(username)))
	message = append(message, username...)
	message = append(message, byte(len(password)))
	message = append(message, password...)
	defer clear(message)
	if err := writeFull(connection, message); err != nil {
		return fmt.Errorf("write SOCKS5 username/password authentication: %w", err)
	}
	var response [2]byte
	if _, err := io.ReadFull(connection, response[:]); err != nil {
		return fmt.Errorf("read SOCKS5 username/password authentication status: %w", err)
	}
	if response[0] != socksAuthVersion || response[1] != 0 {
		return fmt.Errorf("SOCKS5 username/password authentication failed")
	}
	return nil
}

func encodeAddress(address string) ([]byte, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil || host == "" || strings.TrimSpace(address) != address {
		return nil, fmt.Errorf("SOCKS5 destination must be an IPv4 address or DNS name followed by a port")
	}
	portValue, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || portValue == 0 {
		return nil, fmt.Errorf("SOCKS5 destination port must be between 1 and 65535")
	}
	encoded := make([]byte, 0, 1+len(host)+2)
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() == nil || strings.Contains(host, ":") {
			return nil, ErrIPv6Address
		}
		encoded = append(encoded, socksAddressIPv4)
		encoded = append(encoded, ip.To4()...)
	} else {
		if !validHostname(host) || len(host) > 255 {
			return nil, fmt.Errorf("SOCKS5 destination host must be an IPv4 address or DNS name")
		}
		encoded = append(encoded, socksAddressDomain, byte(len(host)))
		encoded = append(encoded, host...)
	}
	var port [2]byte
	binary.BigEndian.PutUint16(port[:], uint16(portValue))
	encoded = append(encoded, port[:]...)
	return encoded, nil
}

func readReply(reader io.Reader) (byte, socksAddress, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return 0, socksAddress{}, fmt.Errorf("read SOCKS5 reply header: %w", err)
	}
	if header[0] != socksVersion {
		return 0, socksAddress{}, fmt.Errorf("SOCKS5 server returned an invalid reply version")
	}
	if header[2] != 0 {
		return 0, socksAddress{}, fmt.Errorf("SOCKS5 server returned a nonzero reserved field")
	}
	address, err := readAddress(reader, header[3])
	if err != nil {
		return 0, socksAddress{}, err
	}
	var port [2]byte
	if _, err := io.ReadFull(reader, port[:]); err != nil {
		return 0, socksAddress{}, fmt.Errorf("read SOCKS5 reply port: %w", err)
	}
	address.Port = binary.BigEndian.Uint16(port[:])
	return header[1], address, nil
}

func readAddress(reader io.Reader, addressType byte) (socksAddress, error) {
	switch addressType {
	case socksAddressIPv4:
		var address [4]byte
		if _, err := io.ReadFull(reader, address[:]); err != nil {
			return socksAddress{}, fmt.Errorf("read SOCKS5 IPv4 address: %w", err)
		}
		return socksAddress{Host: net.IP(address[:]).String()}, nil
	case socksAddressDomain:
		var length [1]byte
		if _, err := io.ReadFull(reader, length[:]); err != nil {
			return socksAddress{}, fmt.Errorf("read SOCKS5 domain length: %w", err)
		}
		if length[0] == 0 {
			return socksAddress{}, fmt.Errorf("SOCKS5 domain address is empty")
		}
		host := make([]byte, int(length[0]))
		if _, err := io.ReadFull(reader, host); err != nil {
			return socksAddress{}, fmt.Errorf("read SOCKS5 domain address: %w", err)
		}
		if !validHostname(string(host)) {
			return socksAddress{}, fmt.Errorf("SOCKS5 server returned an invalid domain address")
		}
		return socksAddress{Host: string(host)}, nil
	case socksAddressIPv6:
		return socksAddress{}, ErrIPv6Address
	default:
		return socksAddress{}, fmt.Errorf("SOCKS5 server returned an unsupported address type")
	}
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func resolveUDPRelay(ctx context.Context, control net.Conn, bound socksAddress) (*net.UDPAddr, error) {
	if bound.Port == 0 {
		return nil, fmt.Errorf("SOCKS5 UDP relay returned port zero")
	}
	if ip := net.ParseIP(bound.Host); ip != nil {
		if ip.To4() == nil {
			return nil, ErrIPv6Address
		}
		if ip.IsUnspecified() {
			remote, ok := control.RemoteAddr().(*net.TCPAddr)
			if !ok || remote.IP.To4() == nil {
				return nil, fmt.Errorf("cannot resolve the SOCKS5 UDP relay wildcard address to the server's IPv4 address")
			}
			ip = remote.IP.To4()
		}
		return &net.UDPAddr{IP: ip.To4(), Port: int(bound.Port)}, nil
	}
	addresses, err := net.DefaultResolver.LookupIP(ctx, "ip4", bound.Host)
	if err != nil {
		return nil, fmt.Errorf("resolve SOCKS5 UDP relay host using IPv4: %w", err)
	}
	for _, address := range addresses {
		if ipv4 := address.To4(); ipv4 != nil {
			return &net.UDPAddr{IP: ipv4, Port: int(bound.Port)}, nil
		}
	}
	return nil, fmt.Errorf("SOCKS5 UDP relay host has no IPv4 address")
}
