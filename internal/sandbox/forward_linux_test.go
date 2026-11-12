//go:build linux

package sandbox

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/arwos/isobox/internal/profile"
)

func TestPastaPIDFileReadyRequiresMatchingPID(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "ready.pid")
	process := &pastaProcess{
		command: &exec.Cmd{Process: &os.Process{Pid: 1234}},
		pidFile: pidFile,
	}

	if err := os.WriteFile(pidFile, []byte("1234\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !process.pidFileReady() {
		t.Fatal("pidFileReady() = false, want a matching PID")
	}
	if err := os.WriteFile(pidFile, []byte("4321\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if process.pidFileReady() {
		t.Fatal("pidFileReady() = true, want false for a different PID")
	}
	if err := os.WriteFile(pidFile, []byte("not a PID\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if process.pidFileReady() {
		t.Fatal("pidFileReady() = true, want false for malformed content")
	}
}

func TestPortForwardSpecs(t *testing.T) {
	rules := []profile.PortRule{
		{HostAddress: "127.0.0.1", Port: 80, Protocol: profile.PortProtocolTCP},
		{HostAddress: "::1", Port: 9000, EndPort: 9002, Protocol: profile.PortProtocolUDP},
		{HostAddress: "192.0.2.10", Port: 443, Protocol: profile.PortProtocolBoth},
	}
	tcp, udp, err := portForwardSpecs(rules)
	if err != nil {
		t.Fatalf("portForwardSpecs() error = %v", err)
	}
	if tcp != "127.0.0.1/80,192.0.2.10/443" || udp != "::1/9000-9002,192.0.2.10/443" {
		t.Fatalf("portForwardSpecs() = (%q, %q), want TCP and UDP profile rules", tcp, udp)
	}
	if _, _, err := portForwardSpecs([]profile.PortRule{{
		HostAddress: "127.0.0.1", Port: 80, Protocol: "invalid",
	}}); err == nil {
		t.Fatal("portForwardSpecs() accepted an invalid protocol")
	}
}

func TestExpectedForwardedSockets(t *testing.T) {
	rules := []profile.PortRule{
		{HostAddress: "127.0.0.1", Port: 80, Protocol: profile.PortProtocolBoth},
		{HostAddress: "::1", Port: 9000, EndPort: 9001, Protocol: profile.PortProtocolUDP},
	}
	got, hasTCP, hasUDP, err := expectedForwardedSockets(rules)
	if err != nil {
		t.Fatalf("expectedForwardedSockets() error = %v", err)
	}
	want := map[string]struct{}{
		"tcp|127.0.0.1|80": {},
		"udp|127.0.0.1|80": {},
		"udp|::1|9000":     {},
		"udp|::1|9001":     {},
	}
	if !hasTCP || !hasUDP || !reflect.DeepEqual(got, want) {
		t.Fatalf("expectedForwardedSockets() = (%#v, %t, %t), want (%#v, true, true)", got, hasTCP, hasUDP, want)
	}
}

func TestParseProcEndpointUsesNativeEndianness(t *testing.T) {
	tests := []struct {
		name  string
		order binary.ByteOrder
		ipv4  string
		ipv6  string
	}{
		{
			name: "little endian", order: binary.LittleEndian,
			ipv4: "0100007F", ipv6: "00000000000000000000000001000000",
		},
		{
			name: "big endian", order: binary.BigEndian,
			ipv4: "7F000001", ipv6: "00000000000000000000000000000001",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, endpoint := range []struct {
				value string
				ip    string
				port  uint16
			}{
				{value: test.ipv4 + ":1F90", ip: "127.0.0.1", port: 8080},
				{value: test.ipv6 + ":2328", ip: "::1", port: 9000},
			} {
				ip, port, err := parseProcEndpointWithByteOrder(endpoint.value, test.order)
				if err != nil || ip != endpoint.ip || port != endpoint.port {
					t.Fatalf("parseProcEndpointWithByteOrder(%q) = (%q, %d, %v), want (%q, %d, nil)", endpoint.value, ip, port, err, endpoint.ip, endpoint.port)
				}
			}
		})
	}
	nativeIPv4 := "7F000001"
	if binary.NativeEndian.Uint16([]byte{1, 0}) == 1 {
		nativeIPv4 = "0100007F"
	}
	if ip, port, err := parseProcEndpoint(nativeIPv4 + ":1F90"); err != nil || ip != "127.0.0.1" || port != 8080 {
		t.Fatalf("parseProcEndpoint() = (%q, %d, %v), want (127.0.0.1, 8080, nil)", ip, port, err)
	}
	for _, value := range []string{"bad:1", "00:xyz", "AA:1"} {
		if _, _, err := parseProcEndpoint(value); err == nil {
			t.Errorf("parseProcEndpoint(%q) accepted an invalid endpoint", value)
		}
	}
}

func TestExpandSlirpHostForwards(t *testing.T) {
	tests := []struct {
		name    string
		rules   []profile.PortRule
		want    []slirpHostForward
		wantErr string
	}{
		{
			name: "both protocols and range",
			rules: []profile.PortRule{{
				HostAddress: "127.0.0.1", Port: 8400, EndPort: 8401, Protocol: profile.PortProtocolBoth,
			}},
			want: []slirpHostForward{
				{Protocol: "tcp", HostAddr: "127.0.0.1", HostPort: 8400, GuestAddr: slirpGuestAddress, GuestPort: 8400},
				{Protocol: "udp", HostAddr: "127.0.0.1", HostPort: 8400, GuestAddr: slirpGuestAddress, GuestPort: 8400},
				{Protocol: "tcp", HostAddr: "127.0.0.1", HostPort: 8401, GuestAddr: slirpGuestAddress, GuestPort: 8401},
				{Protocol: "udp", HostAddr: "127.0.0.1", HostPort: 8401, GuestAddr: slirpGuestAddress, GuestPort: 8401},
			},
		},
		{
			name: "TCP single port",
			rules: []profile.PortRule{{
				HostAddress: "192.0.2.10", Port: 443, Protocol: profile.PortProtocolTCP,
			}},
			want: []slirpHostForward{{
				Protocol: "tcp", HostAddr: "192.0.2.10", HostPort: 443, GuestAddr: slirpGuestAddress, GuestPort: 443,
			}},
		},
		{
			name: "UDP single port",
			rules: []profile.PortRule{{
				HostAddress: "192.0.2.10", Port: 53, Protocol: profile.PortProtocolUDP,
			}},
			want: []slirpHostForward{{
				Protocol: "udp", HostAddr: "192.0.2.10", HostPort: 53, GuestAddr: slirpGuestAddress, GuestPort: 53,
			}},
		},
		{
			name: "reject IPv6 host address",
			rules: []profile.PortRule{{
				HostAddress: "::1", Port: 80, Protocol: profile.PortProtocolTCP,
			}},
			wantErr: "not supported by slirp4netns",
		},
		{
			name: "reject invalid protocol",
			rules: []profile.PortRule{{
				HostAddress: "127.0.0.1", Port: 80, Protocol: "icmp",
			}},
			wantErr: "protocol \"icmp\" is invalid",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := expandSlirpHostForwards(test.rules)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("expandSlirpHostForwards() error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("expandSlirpHostForwards() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("expandSlirpHostForwards() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestSlirpHostForwardRequest(t *testing.T) {
	forward := slirpHostForward{
		Protocol: "tcp", HostAddr: "127.0.0.1", HostPort: 8400, GuestAddr: slirpGuestAddress, GuestPort: 8400,
	}
	encoded, err := slirpHostForwardRequest(forward)
	if err != nil {
		t.Fatalf("slirpHostForwardRequest() error = %v", err)
	}
	var got struct {
		Execute   string           `json:"execute"`
		Arguments slirpHostForward `json:"arguments"`
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if got.Execute != "add_hostfwd" || got.Arguments != forward {
		t.Fatalf("slirp request = (%q, %#v), want (add_hostfwd, %#v)", got.Execute, got.Arguments, forward)
	}
}

func TestValidateSlirpHostForwardResponse(t *testing.T) {
	tests := []struct {
		name     string
		response string
		wantErr  string
	}{
		{name: "confirmed", response: `{"return":{"id":1}}`},
		{name: "API error", response: `{"error":{"class":"GenericError","desc":"address in use"}}`, wantErr: "address in use"},
		{name: "missing return", response: `{}`, wantErr: "no return value"},
		{name: "invalid ID", response: `{"return":{"id":0}}`, wantErr: "did not confirm"},
		{name: "invalid JSON", response: `{`, wantErr: "decode API response"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateSlirpHostForwardResponse([]byte(test.response))
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("validateSlirpHostForwardResponse() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("validateSlirpHostForwardResponse() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}
