package preflight

import (
	"errors"
	"testing"
)

func TestBubblewrapFailureAdvice(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "network route netlink denied",
			err:  errors.New("bwrap: loopback: Failed to create NETLINK_ROUTE socket: Operation not permitted"),
			want: "allow rootless bwrap to configure loopback in its network namespace; check host/container policy that restricts NETLINK_ROUTE access",
		},
		{
			name: "other bwrap failure",
			err:  errors.New("minimal rootless sandbox probe failed: exit status 1"),
			want: "verify the bubblewrap installation and version, then repair or update it",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := bubblewrapFailureAdvice(test.err); got != test.want {
				t.Fatalf("bubblewrapFailureAdvice() = %q, want %q", got, test.want)
			}
		})
	}
}
