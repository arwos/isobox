package proxy

import (
	"context"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	singBoxFixtureEnv     = "ISOBOX_SINGBOX_FIXTURE"
	singBoxFixtureUser    = "isobox-test"
	singBoxFixturePass    = "local-fixture-password"
	singBoxFixtureBadPass = "wrong-fixture-password"
)

func TestSOCKS5PodmanFixture(t *testing.T) {
	endpoint := os.Getenv(singBoxFixtureEnv)
	if endpoint == "" {
		t.Skipf("set %s to run the Podman sing-box fixture", singBoxFixtureEnv)
	}

	client := newSingBoxFixtureClient(t, endpoint, singBoxFixturePass)
	waitForSingBoxFixture(t, client)

	t.Run("TCP CONNECT", func(t *testing.T) {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen for local TCP echo: %v", err)
		}
		t.Cleanup(func() { _ = listener.Close() })
		go func() {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			defer connection.Close()
			payload := make([]byte, len("isobox TCP fixture"))
			if _, err := io.ReadFull(connection, payload); err == nil {
				_, _ = connection.Write(payload)
			}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		connection, err := client.DialContext(ctx, "tcp4", listener.Addr().String())
		if err != nil {
			t.Fatalf("SOCKS5 CONNECT: %v", err)
		}
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.WriteString(connection, "isobox TCP fixture"); err != nil {
			t.Fatalf("write through CONNECT: %v", err)
		}
		response := make([]byte, len("isobox TCP fixture"))
		if _, err := io.ReadFull(connection, response); err != nil {
			t.Fatalf("read through CONNECT: %v", err)
		}
		if string(response) != "isobox TCP fixture" {
			t.Fatalf("TCP echo = %q", response)
		}
	})

	t.Run("UDP ASSOCIATE", func(t *testing.T) {
		echo, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatalf("listen for local UDP echo: %v", err)
		}
		t.Cleanup(func() { _ = echo.Close() })
		go func() {
			buffer := make([]byte, 65507)
			for {
				length, source, err := echo.ReadFromUDP(buffer)
				if err != nil {
					return
				}
				if _, err := echo.WriteToUDP(buffer[:length], source); err != nil {
					return
				}
			}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		association, err := client.UDPAssociate(ctx)
		if err != nil {
			t.Fatalf("SOCKS5 UDP ASSOCIATE: %v", err)
		}
		defer association.Close()
		if err := association.SendTo(echo.LocalAddr().String(), []byte("isobox UDP fixture")); err != nil {
			t.Fatalf("send through UDP relay: %v", err)
		}
		_ = association.SetReadDeadline(time.Now().Add(5 * time.Second))
		buffer := make([]byte, 65507)
		length, source, err := association.ReadFrom(buffer)
		if err != nil {
			t.Fatalf("receive through UDP relay: %v", err)
		}
		if string(buffer[:length]) != "isobox UDP fixture" {
			t.Fatalf("UDP echo = %q", buffer[:length])
		}
		if source != echo.LocalAddr().String() {
			t.Fatalf("UDP source = %q, want %q", source, echo.LocalAddr())
		}
	})

	t.Run("authentication rejection", func(t *testing.T) {
		badClient := newSingBoxFixtureClient(t, endpoint, singBoxFixtureBadPass)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := badClient.probe(ctx); err == nil {
			t.Fatal("SOCKS5 accepted an incorrect password")
		} else if strings.Contains(err.Error(), singBoxFixtureBadPass) || strings.Contains(err.Error(), singBoxFixtureUser) {
			t.Fatalf("authentication error disclosed credentials: %v", err)
		}
	})
}

func TestSOCKS5PodmanFixtureStopped(t *testing.T) {
	if os.Getenv("ISOBOX_SINGBOX_EXPECT_DOWN") == "" {
		t.Skip("set ISOBOX_SINGBOX_EXPECT_DOWN after stopping the Podman fixture")
	}
	endpoint := os.Getenv(singBoxFixtureEnv)
	if endpoint == "" {
		t.Fatalf("%s is required when checking fixture shutdown", singBoxFixtureEnv)
	}
	client := newSingBoxFixtureClient(t, endpoint, singBoxFixturePass)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.probe(ctx); err == nil {
		t.Fatal("SOCKS5 fixture still accepts connections after Podman stop")
	}
}

func newSingBoxFixtureClient(t *testing.T, endpoint, password string) *socks5Client {
	t.Helper()
	client, err := newSOCKS5Client(runtimeConfig{
		server:   endpoint,
		auth:     AuthUserPass,
		username: singBoxFixtureUser,
		password: password,
	})
	if err != nil {
		t.Fatalf("create fixture client: %v", err)
	}
	client.timeout = 2 * time.Second
	return client
}

func waitForSingBoxFixture(t *testing.T, client *socks5Client) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		lastErr = client.probe(ctx)
		cancel()
		if lastErr == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("sing-box SOCKS5 fixture did not become ready: %v", lastErr)
}
