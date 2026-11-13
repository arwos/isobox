package proxy

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type dialerFunc func(context.Context, string, string) (net.Conn, error)

func (dialer dialerFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return dialer(ctx, network, address)
}

type countedPipeDialer struct {
	client  net.Conn
	calls   int
	network string
	address string
	dialErr error
}

func (dialer *countedPipeDialer) DialContext(_ context.Context, network, address string) (net.Conn, error) {
	dialer.calls++
	dialer.network = network
	dialer.address = address
	if dialer.dialErr != nil {
		return nil, dialer.dialErr
	}
	return dialer.client, nil
}

func newPipeClient(auth AuthMode, username, password string) (*socks5Client, net.Conn, *countedPipeDialer) {
	clientSide, serverSide := net.Pipe()
	dialer := &countedPipeDialer{client: clientSide}
	return &socks5Client{
		server:   "proxy.example:1080",
		auth:     auth,
		username: username,
		password: password,
		dialer:   dialer,
		timeout:  2 * time.Second,
	}, serverSide, dialer
}

func TestSOCKS5ProbeNegotiatesWithoutOpeningDestination(t *testing.T) {
	client, serverSide, dialer := newPipeClient(AuthNone, "", "")
	serverResult := make(chan error, 1)
	go func() {
		defer serverSide.Close()
		if err := expectGreeting(serverSide, socksMethodNone); err != nil {
			serverResult <- err
			return
		}
		if err := writeFull(serverSide, []byte{socksVersion, socksMethodNone}); err != nil {
			serverResult <- err
			return
		}
		var extra [1]byte
		if _, err := serverSide.Read(extra[:]); !errors.Is(err, io.EOF) {
			serverResult <- errors.New("SOCKS5 readiness probe sent a destination command or kept its control connection open")
			return
		}
		serverResult <- nil
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.probe(ctx); err != nil {
		t.Fatalf("probe() error = %v", err)
	}
	if dialer.calls != 1 || dialer.network != "tcp4" {
		t.Fatalf("probe dial = calls %d, network %q; want one IPv4 TCP connection", dialer.calls, dialer.network)
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
}

func TestSOCKS5ConnectNoAuthDomainAndIPv4(t *testing.T) {
	tests := []struct {
		name        string
		destination string
		wantHost    string
		wantPort    uint16
	}{
		{name: "domain", destination: "example.org:443", wantHost: "example.org", wantPort: 443},
		{name: "ipv4", destination: "192.0.2.5:8443", wantHost: "192.0.2.5", wantPort: 8443},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, serverSide, dialer := newPipeClient(AuthNone, "", "")
			serverResult := make(chan error, 1)
			go func() {
				defer serverSide.Close()
				if err := expectGreeting(serverSide, socksMethodNone); err != nil {
					serverResult <- err
					return
				}
				if err := writeFull(serverSide, []byte{socksVersion, socksMethodNone}); err != nil {
					serverResult <- err
					return
				}
				command, target, err := readRequest(serverSide)
				if err != nil {
					serverResult <- err
					return
				}
				if command != socksCommandConnect || target.Host != test.wantHost || target.Port != test.wantPort {
					serverResult <- errors.New("unexpected CONNECT request")
					return
				}
				if err := writeReplyCode(serverSide, 0, "127.0.0.1:50000"); err != nil {
					serverResult <- err
					return
				}
				serverResult <- echoPipe(serverSide)
			}()

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			connection, err := client.DialContext(ctx, "tcp", test.destination)
			if err != nil {
				t.Fatal(err)
			}
			if dialer.calls != 1 || dialer.network != "tcp4" || dialer.address != "proxy.example:1080" {
				t.Fatalf("proxy dial = calls %d, network %q, address %q", dialer.calls, dialer.network, dialer.address)
			}
			if err := writeFull(connection, []byte("ping")); err != nil {
				t.Fatal(err)
			}
			response := make([]byte, 4)
			if _, err := io.ReadFull(connection, response); err != nil {
				t.Fatal(err)
			}
			if string(response) != "ping" {
				t.Fatalf("echo response = %q", response)
			}
			_ = connection.Close()
			if err := <-serverResult; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSOCKS5ConnectUsernamePassword(t *testing.T) {
	client, serverSide, dialer := newPipeClient(AuthUserPass, "test-user", "test-password")
	serverResult := make(chan error, 1)
	go func() {
		defer serverSide.Close()
		if err := expectGreeting(serverSide, socksMethodUser); err != nil {
			serverResult <- err
			return
		}
		if err := writeFull(serverSide, []byte{socksVersion, socksMethodUser}); err != nil {
			serverResult <- err
			return
		}
		username, password, err := readCredentials(serverSide)
		if err != nil {
			serverResult <- err
			return
		}
		if username != "test-user" || password != "test-password" {
			serverResult <- errors.New("SOCKS5 username/password payload differed")
			return
		}
		if err := writeFull(serverSide, []byte{socksAuthVersion, 0}); err != nil {
			serverResult <- err
			return
		}
		command, target, err := readRequest(serverSide)
		if err != nil {
			serverResult <- err
			return
		}
		if command != socksCommandConnect || target.Host != "example.org" || target.Port != 443 {
			serverResult <- errors.New("unexpected authenticated CONNECT request")
			return
		}
		if err := writeReplyCode(serverSide, 0, "127.0.0.1:50001"); err != nil {
			serverResult <- err
			return
		}
		serverResult <- echoPipe(serverSide)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	connection, err := client.DialContext(ctx, "tcp4", "example.org:443")
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if dialer.calls != 1 {
		t.Fatalf("SOCKS server dials = %d, want exactly one", dialer.calls)
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
}

func TestSOCKS5AuthenticationFailureDoesNotExposeCredentials(t *testing.T) {
	client, serverSide, dialer := newPipeClient(AuthUserPass, "private-user", "private-password")
	serverResult := make(chan error, 1)
	go func() {
		defer serverSide.Close()
		if err := expectGreeting(serverSide, socksMethodUser); err != nil {
			serverResult <- err
			return
		}
		if err := writeFull(serverSide, []byte{socksVersion, socksMethodUser}); err != nil {
			serverResult <- err
			return
		}
		username, password, err := readCredentials(serverSide)
		if err != nil {
			serverResult <- err
			return
		}
		if username != "private-user" || password != "private-password" {
			serverResult <- errors.New("SOCKS5 username/password payload differed")
			return
		}
		serverResult <- writeFull(serverSide, []byte{socksAuthVersion, 1})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := client.DialContext(ctx, "tcp", "example.org:443"); err == nil {
		t.Fatal("DialContext succeeded after SOCKS5 authentication failure")
	} else if strings.Contains(err.Error(), "private-user") || strings.Contains(err.Error(), "private-password") {
		t.Fatalf("authentication error disclosed credentials: %v", err)
	}
	if dialer.calls != 1 {
		t.Fatalf("proxy dials = %d, want no retry", dialer.calls)
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
}

func TestSOCKS5RejectsMalformedNegotiationAndReply(t *testing.T) {
	t.Run("invalid method version", func(t *testing.T) {
		client, serverSide, dialer := newPipeClient(AuthNone, "", "")
		serverResult := make(chan error, 1)
		go func() {
			defer serverSide.Close()
			if err := expectGreeting(serverSide, socksMethodNone); err != nil {
				serverResult <- err
				return
			}
			serverResult <- writeFull(serverSide, []byte{4, socksMethodNone})
		}()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, err := client.DialContext(ctx, "tcp", "example.org:443"); err == nil || !strings.Contains(err.Error(), "invalid method-selection version") {
			t.Fatalf("malformed method selection error = %v", err)
		}
		if dialer.calls != 1 {
			t.Fatalf("proxy dials = %d, want no retry", dialer.calls)
		}
		if err := <-serverResult; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("invalid reply version", func(t *testing.T) {
		client, serverSide, dialer := newPipeClient(AuthNone, "", "")
		serverResult := make(chan error, 1)
		go func() {
			defer serverSide.Close()
			if err := expectGreeting(serverSide, socksMethodNone); err != nil {
				serverResult <- err
				return
			}
			if err := writeFull(serverSide, []byte{socksVersion, socksMethodNone}); err != nil {
				serverResult <- err
				return
			}
			if _, _, err := readRequest(serverSide); err != nil {
				serverResult <- err
				return
			}
			serverResult <- writeFull(serverSide, []byte{4, 0, 0, socksAddressIPv4})
		}()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, err := client.DialContext(ctx, "tcp", "example.org:443"); err == nil || !strings.Contains(err.Error(), "invalid reply version") {
			t.Fatalf("malformed SOCKS reply error = %v", err)
		}
		if dialer.calls != 1 {
			t.Fatalf("proxy dials = %d, want no retry", dialer.calls)
		}
		if err := <-serverResult; err != nil {
			t.Fatal(err)
		}
	})
}

func TestSOCKS5DoesNotDowngradeAuthenticationOrFallBackDirectly(t *testing.T) {
	t.Run("authentication downgrade", func(t *testing.T) {
		client, serverSide, dialer := newPipeClient(AuthUserPass, "user", "password")
		serverResult := make(chan error, 1)
		go func() {
			defer serverSide.Close()
			if err := expectGreeting(serverSide, socksMethodUser); err != nil {
				serverResult <- err
				return
			}
			serverResult <- writeFull(serverSide, []byte{socksVersion, socksMethodNone})
		}()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, err := client.DialContext(ctx, "tcp", "example.org:443"); err == nil || !strings.Contains(err.Error(), "not offered") {
			t.Fatalf("downgrade error = %v", err)
		}
		if err := <-serverResult; err != nil {
			t.Fatal(err)
		}
		if dialer.calls != 1 {
			t.Fatalf("proxy dials = %d, want 1", dialer.calls)
		}
	})

	t.Run("proxy rejection", func(t *testing.T) {
		client, serverSide, dialer := newPipeClient(AuthNone, "", "")
		serverResult := make(chan error, 1)
		go func() {
			defer serverSide.Close()
			if err := expectGreeting(serverSide, socksMethodNone); err != nil {
				serverResult <- err
				return
			}
			if err := writeFull(serverSide, []byte{socksVersion, socksMethodNone}); err != nil {
				serverResult <- err
				return
			}
			if _, _, err := readRequest(serverSide); err != nil {
				serverResult <- err
				return
			}
			serverResult <- writeReplyCode(serverSide, 5, "127.0.0.1:1")
		}()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := client.DialContext(ctx, "tcp", "203.0.113.8:443")
		var replyError *ReplyError
		if !errors.As(err, &replyError) || replyError.Code != 5 {
			t.Fatalf("proxy rejection error = %v", err)
		}
		if dialer.calls != 1 || dialer.network != "tcp4" {
			t.Fatalf("proxy dial after rejection = %#v", dialer)
		}
		if err := <-serverResult; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("server dial failure", func(t *testing.T) {
		client, serverSide, dialer := newPipeClient(AuthNone, "", "")
		_ = serverSide.Close()
		_ = dialer.client.Close()
		dialer.dialErr = errors.New("proxy unavailable")
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, err := client.DialContext(ctx, "tcp", "203.0.113.8:443"); err == nil {
			t.Fatal("DialContext succeeded without a SOCKS server")
		}
		if dialer.calls != 1 {
			t.Fatalf("dial count = %d, want no retry", dialer.calls)
		}
	})
}

func TestSOCKS5RejectsIPv6AndInvalidDestinationBeforeDial(t *testing.T) {
	for _, destination := range []string{"[2001:db8::1]:443", "bad..example:443", "bad host:443", "example.org:0"} {
		client, serverSide, dialer := newPipeClient(AuthNone, "", "")
		_ = serverSide.Close()
		_ = dialer.client.Close()
		_, err := client.DialContext(context.Background(), "tcp", destination)
		if err == nil {
			t.Errorf("DialContext(%q) succeeded", destination)
		}
		if dialer.calls != 0 {
			t.Errorf("DialContext(%q) dialed proxy %d times before validating target", destination, dialer.calls)
		}
	}
	client, serverSide, dialer := newPipeClient(AuthNone, "", "")
	_ = serverSide.Close()
	_ = dialer.client.Close()
	if _, err := client.DialContext(context.Background(), "tcp6", "example.org:443"); err == nil {
		t.Fatal("DialContext accepted tcp6")
	}
	if dialer.calls != 0 {
		t.Fatalf("unsupported network dial count = %d", dialer.calls)
	}
}

func TestSOCKS5ConnectCancellationClosesHandshake(t *testing.T) {
	client, serverSide, dialer := newPipeClient(AuthNone, "", "")
	serverRead := make(chan error, 1)
	go func() {
		defer serverSide.Close()
		serverRead <- expectGreeting(serverSide, socksMethodNone)
		var wait [1]byte
		_, _ = serverSide.Read(wait[:])
	}()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.DialContext(ctx, "tcp", "example.org:443")
		result <- err
	}()
	if err := <-serverRead; err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled DialContext error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled SOCKS5 handshake did not return")
	}
	if dialer.calls != 1 {
		t.Fatalf("proxy dial count = %d", dialer.calls)
	}
}

func TestSOCKS5UDPAssociateRelaysOnlyThroughReturnedRelay(t *testing.T) {
	client, packetSocket, relayAddress, controlPeer, dialer, serverResult := newUDPTestServer(t, AuthUserPass, "udp-user", "udp-password", "127.0.0.1")
	defer controlPeer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	association, err := client.UDPAssociate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer association.Close()
	if dialer.calls != 1 || dialer.network != "tcp4" {
		t.Fatalf("control connection dial = calls %d, network %q", dialer.calls, dialer.network)
	}
	if err := association.SendTo("example.org:5353", []byte("query")); err != nil {
		t.Fatal(err)
	}
	request := <-packetSocket.outbound
	destination, payloadOffset, fragment, err := decodeUDPHeader(request.data)
	if err != nil {
		t.Fatal(err)
	}
	if destination.Host != "example.org" || destination.Port != 5353 || fragment != 0 || string(request.data[payloadOffset:]) != "query" {
		t.Fatalf("relay received destination=%#v fragment=%d payload=%q", destination, fragment, request.data[payloadOffset:])
	}
	if !sameUDPAddress(request.address, relayAddress) {
		t.Fatalf("client sent UDP to %v, not SOCKS relay %v", request.address, relayAddress)
	}
	packetSocket.inject(udpResponse(t, "203.0.113.10:53", []byte("spoof")), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 41001})
	packetSocket.inject(udpResponse(t, "203.0.113.10:53", []byte("answer")), relayAddress)
	answer := make([]byte, 65507)
	_ = association.SetReadDeadline(time.Now().Add(time.Second))
	answerLength, replySource, err := association.ReadFrom(answer)
	if err != nil {
		t.Fatal(err)
	}
	if replySource != "203.0.113.10:53" || string(answer[:answerLength]) != "answer" {
		t.Fatalf("UDP response source=%q payload=%q", replySource, answer[:answerLength])
	}
	if err := association.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}
}

func TestSOCKS5UDPRejectsIPv6AndFragmentedPackets(t *testing.T) {
	client, packetSocket, relayAddress, controlPeer, _, serverResult := newUDPTestServer(t, AuthNone, "", "", "127.0.0.1")
	defer controlPeer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	association, err := client.UDPAssociate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer association.Close()
	fragmented := udpResponse(t, "192.0.2.2:53", []byte("frag"))
	fragmented[2] = 1
	packetSocket.inject(fragmented, relayAddress)
	readBuffer := make([]byte, 65507)
	_ = association.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := association.ReadFrom(readBuffer); !errors.Is(err, ErrUnsupportedFragment) {
		t.Fatalf("fragmented UDP error = %v", err)
	}
	ipv6 := []byte{0, 0, 0, socksAddressIPv6}
	ipv6 = append(ipv6, make([]byte, 16)...)
	ipv6 = append(ipv6, 0, 53)
	packetSocket.inject(ipv6, relayAddress)
	_ = association.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := association.ReadFrom(readBuffer); !errors.Is(err, ErrIPv6Address) {
		t.Fatalf("IPv6 UDP reply error = %v", err)
	}
	_ = association.Close()
	if err := <-serverResult; err != nil {
		t.Fatal(err)
	}

	if err := association.SendTo("[2001:db8::1]:53", []byte("x")); !errors.Is(err, ErrIPv6Address) {
		t.Fatalf("IPv6 UDP destination error = %v", err)
	}
}

func TestSOCKS5UDPAssociationEndsWithControlConnectionOrContext(t *testing.T) {
	t.Run("control connection", func(t *testing.T) {
		client, _, _, controlPeer, dialer, serverResult := newUDPTestServer(t, AuthNone, "", "", "127.0.0.1")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		association, err := client.UDPAssociate(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := controlPeer.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-association.Done():
		case <-time.After(time.Second):
			t.Fatal("association stayed open after control connection closed")
		}
		if association.Err() == nil {
			t.Fatal("association did not expose control-connection failure")
		}
		if err := association.SendTo("198.51.100.4:443", []byte("x")); err == nil {
			t.Fatal("closed association sent data")
		}
		if dialer.calls != 1 {
			t.Fatalf("proxy dial count = %d, want no fallback", dialer.calls)
		}
		if err := <-serverResult; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		client, _, _, controlPeer, _, serverResult := newUDPTestServer(t, AuthNone, "", "", "127.0.0.1")
		defer controlPeer.Close()
		ctx, cancel := context.WithCancel(context.Background())
		association, err := client.UDPAssociate(ctx)
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		select {
		case <-association.Done():
		case <-time.After(time.Second):
			t.Fatal("association stayed open after context cancellation")
		}
		if !errors.Is(association.Err(), context.Canceled) {
			t.Fatalf("association context error = %v", association.Err())
		}
		if err := <-serverResult; err != nil {
			t.Fatal(err)
		}
	})
}

func TestSOCKS5RejectsUnsupportedUDPRelayResponse(t *testing.T) {
	for _, test := range []struct {
		name string
		rep  byte
		addr string
		ipv6 bool
	}{
		{name: "server refusal", rep: 2, addr: "127.0.0.1:12345"},
		{name: "ipv6 relay", rep: 0, ipv6: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			clientSide, serverSide := net.Pipe()
			dialer := dialerFunc(func(_ context.Context, network, address string) (net.Conn, error) {
				if network != "tcp4" || address != "proxy.example:1080" {
					return nil, errors.New("unexpected proxy dial target")
				}
				return clientSide, nil
			})
			client := &socks5Client{server: "proxy.example:1080", auth: AuthNone, dialer: dialer, timeout: time.Second}
			serverResult := make(chan error, 1)
			go func() {
				defer serverSide.Close()
				if err := expectGreeting(serverSide, socksMethodNone); err != nil {
					serverResult <- err
					return
				}
				if err := writeFull(serverSide, []byte{socksVersion, socksMethodNone}); err != nil {
					serverResult <- err
					return
				}
				command, address, err := readRequest(serverSide)
				if err != nil {
					serverResult <- err
					return
				}
				if command != socksCommandUDP || address.Host != "0.0.0.0" || address.Port != 0 {
					serverResult <- errors.New("unexpected UDP ASSOCIATE request")
					return
				}
				if test.ipv6 {
					serverResult <- writeFull(serverSide, []byte{socksVersion, test.rep, 0, socksAddressIPv6})
					return
				}
				serverResult <- writeReplyCode(serverSide, test.rep, test.addr)
			}()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			association, err := client.UDPAssociate(ctx)
			if association != nil {
				_ = association.Close()
			}
			if err == nil {
				t.Fatal("UDPAssociate succeeded with an invalid response")
			}
			if test.name == "ipv6 relay" && !errors.Is(err, ErrIPv6Address) {
				t.Fatalf("IPv6 relay error = %v", err)
			}
			if err := <-serverResult; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func newUDPTestServer(t *testing.T, auth AuthMode, username, password, relayHost string) (*socks5Client, *mockUDPPacketConn, *net.UDPAddr, net.Conn, *countedPipeDialer, <-chan error) {
	t.Helper()
	client, controlPeer, dialer := newPipeClient(auth, username, password)
	packetSocket := newMockUDPPacketConn()
	relayAddress := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 39080}
	client.listenUDP = func() (udpPacketConn, error) { return packetSocket, nil }
	result := make(chan error, 1)
	go func() {
		defer controlPeer.Close()
		if err := expectGreeting(controlPeer, methodForAuth(auth)); err != nil {
			result <- err
			return
		}
		if err := writeFull(controlPeer, []byte{socksVersion, methodForAuth(auth)}); err != nil {
			result <- err
			return
		}
		if auth == AuthUserPass {
			gotUser, gotPassword, err := readCredentials(controlPeer)
			if err != nil {
				result <- err
				return
			}
			if gotUser != username || gotPassword != password {
				result <- errors.New("SOCKS5 UDP authentication payload differed")
				return
			}
			if err := writeFull(controlPeer, []byte{socksAuthVersion, 0}); err != nil {
				result <- err
				return
			}
		}
		command, address, err := readRequest(controlPeer)
		if err != nil {
			result <- err
			return
		}
		if command != socksCommandUDP || address.Host != "0.0.0.0" || address.Port != 0 {
			result <- errors.New("unexpected UDP ASSOCIATE request")
			return
		}
		if err := writeReplyCode(controlPeer, 0, net.JoinHostPort(relayHost, strconv.Itoa(relayAddress.Port))); err != nil {
			result <- err
			return
		}
		var wait [1]byte
		_, err = controlPeer.Read(wait[:])
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.ErrClosedPipe) {
			result <- err
			return
		}
		result <- nil
	}()
	return client, packetSocket, relayAddress, controlPeer, dialer, result
}

type mockUDPDatagram struct {
	data    []byte
	address *net.UDPAddr
}

type mockUDPPacketConn struct {
	inbound  chan mockUDPDatagram
	outbound chan mockUDPDatagram
	done     chan struct{}
	close    sync.Once
	mu       sync.Mutex
	deadline time.Time
}

func newMockUDPPacketConn() *mockUDPPacketConn {
	return &mockUDPPacketConn{
		inbound:  make(chan mockUDPDatagram, 8),
		outbound: make(chan mockUDPDatagram, 8),
		done:     make(chan struct{}),
	}
}

func (connection *mockUDPPacketConn) inject(data []byte, address *net.UDPAddr) {
	connection.inbound <- mockUDPDatagram{data: append([]byte(nil), data...), address: cloneUDPAddress(address)}
}

func (connection *mockUDPPacketConn) ReadFromUDP(buffer []byte) (int, *net.UDPAddr, error) {
	connection.mu.Lock()
	deadline := connection.deadline
	connection.mu.Unlock()
	if deadline.IsZero() {
		select {
		case datagram := <-connection.inbound:
			return copyUDPDatagram(buffer, datagram)
		case <-connection.done:
			return 0, nil, net.ErrClosed
		}
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case datagram := <-connection.inbound:
		return copyUDPDatagram(buffer, datagram)
	case <-connection.done:
		return 0, nil, net.ErrClosed
	case <-timer.C:
		return 0, nil, &net.OpError{Op: "read", Net: "udp4", Err: context.DeadlineExceeded}
	}
}

func (connection *mockUDPPacketConn) WriteToUDP(data []byte, address *net.UDPAddr) (int, error) {
	datagram := mockUDPDatagram{data: append([]byte(nil), data...), address: cloneUDPAddress(address)}
	select {
	case connection.outbound <- datagram:
		return len(data), nil
	case <-connection.done:
		return 0, net.ErrClosed
	}
}

func (connection *mockUDPPacketConn) SetReadDeadline(deadline time.Time) error {
	connection.mu.Lock()
	connection.deadline = deadline
	connection.mu.Unlock()
	return nil
}

func (connection *mockUDPPacketConn) Close() error {
	connection.close.Do(func() { close(connection.done) })
	return nil
}

func copyUDPDatagram(buffer []byte, datagram mockUDPDatagram) (int, *net.UDPAddr, error) {
	return copy(buffer, datagram.data), cloneUDPAddress(datagram.address), nil
}

func cloneUDPAddress(address *net.UDPAddr) *net.UDPAddr {
	if address == nil {
		return nil
	}
	return &net.UDPAddr{IP: append(net.IP(nil), address.IP...), Port: address.Port, Zone: address.Zone}
}

func methodForAuth(auth AuthMode) byte {
	if auth == AuthUserPass {
		return socksMethodUser
	}
	return socksMethodNone
}

func expectGreeting(reader io.Reader, method byte) error {
	var greeting [3]byte
	if _, err := io.ReadFull(reader, greeting[:]); err != nil {
		return err
	}
	if greeting[0] != socksVersion || greeting[1] != 1 || greeting[2] != method {
		return errors.New("unexpected SOCKS5 greeting")
	}
	return nil
}

func readCredentials(reader io.Reader) (string, string, error) {
	var header [2]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return "", "", err
	}
	if header[0] != socksAuthVersion || header[1] == 0 {
		return "", "", errors.New("invalid SOCKS5 credential header")
	}
	username := make([]byte, int(header[1]))
	if _, err := io.ReadFull(reader, username); err != nil {
		return "", "", err
	}
	var passwordLength [1]byte
	if _, err := io.ReadFull(reader, passwordLength[:]); err != nil {
		return "", "", err
	}
	if passwordLength[0] == 0 {
		return "", "", errors.New("empty SOCKS5 password")
	}
	password := make([]byte, int(passwordLength[0]))
	if _, err := io.ReadFull(reader, password); err != nil {
		return "", "", err
	}
	return string(username), string(password), nil
}

func readRequest(reader io.Reader) (byte, socksAddress, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return 0, socksAddress{}, err
	}
	if header[0] != socksVersion || header[2] != 0 {
		return 0, socksAddress{}, errors.New("invalid SOCKS5 request header")
	}
	address, err := readAddress(reader, header[3])
	if err != nil {
		return 0, socksAddress{}, err
	}
	var port [2]byte
	if _, err := io.ReadFull(reader, port[:]); err != nil {
		return 0, socksAddress{}, err
	}
	address.Port = binary.BigEndian.Uint16(port[:])
	return header[1], address, nil
}

func writeReplyCode(writer io.Writer, replyCode byte, bound string) error {
	encoded, err := encodeAddress(bound)
	if err != nil {
		return err
	}
	response := []byte{socksVersion, replyCode, 0}
	response = append(response, encoded...)
	return writeFull(writer, response)
}

func echoPipe(connection net.Conn) error {
	buffer := make([]byte, 128)
	for {
		length, err := connection.Read(buffer)
		if length > 0 {
			if writeErr := writeFull(connection, buffer[:length]); writeErr != nil {
				return writeErr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func udpResponse(t *testing.T, source string, payload []byte) []byte {
	t.Helper()
	address, err := encodeAddress(source)
	if err != nil {
		t.Fatal(err)
	}
	packet := []byte{0, 0, 0}
	packet = append(packet, address...)
	packet = append(packet, payload...)
	return packet
}
