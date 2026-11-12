package preflight

import (
	"errors"
	"os"
	"path/filepath"
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

func TestInspectOptionsProbesFlagsMissingFromHelp(t *testing.T) {
	helper := filepath.Join(t.TempDir(), "option-helper")
	script := `#!/bin/sh
if [ "$1" = "--help" ]; then
	printf '%s\n' '--visible'
	exit 0
fi
if [ "$1" = "--hidden" ] && [ "$2" = "value" ] && [ "$3" = "--help" ]; then
	exit 0
fi
if [ "$1" = "--missing" ]; then
	echo 'unknown option --missing' >&2
	exit 2
fi
exit 3
`
	if err := os.WriteFile(helper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	options, inspectionError := inspectOptions(helper,
		optionProbe{option: "--hidden", args: []string{"--hidden", "value"}},
		optionProbe{option: "--missing", args: []string{"--missing"}},
	)
	if inspectionError != "" {
		t.Fatalf("inspectOptions() error = %q, want no inspection error", inspectionError)
	}
	if !options["--visible"] || !options["--hidden"] {
		t.Fatalf("inspectOptions() = %#v, want help and parser-probed options", options)
	}
	if options["--missing"] {
		t.Fatalf("inspectOptions() marked a parser-rejected option as supported: %#v", options)
	}
}
