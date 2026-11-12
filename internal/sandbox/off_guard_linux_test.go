//go:build linux

package sandbox

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestParsePortSet(t *testing.T) {
	tests := []struct {
		value   string
		want    []string
		wantErr bool
	}{
		{value: "-"},
		{value: "80,0080,100-102,100-102", want: []string{"80", "100-102"}},
		{value: "65535", want: []string{"65535"}},
		{value: "", wantErr: true},
		{value: "0", wantErr: true},
		{value: "65536", wantErr: true},
		{value: "10-9", wantErr: true},
		{value: "20-", wantErr: true},
		{value: "1-2-3", wantErr: true},
	}
	for _, test := range tests {
		got, err := parsePortSet(test.value)
		if (err != nil) != test.wantErr {
			t.Fatalf("parsePortSet(%q) error = %v, wantErr %v", test.value, err, test.wantErr)
		}
		if err == nil && !reflect.DeepEqual(got, test.want) {
			t.Fatalf("parsePortSet(%q) = %#v, want %#v", test.value, got, test.want)
		}
	}
}

func TestBuildOffFilterRules(t *testing.T) {
	got := buildOffFilterRules([]string{"80", "100-102"}, []string{"53"})
	want := `table inet isobox {
 chain input { type filter hook input priority -150; policy drop;
  iifname "lo" accept
  ct state established accept
  tcp dport { 80, 100-102 } ct state new accept
  udp dport { 53 } ct state new accept
 }
 chain output { type filter hook output priority -150; policy drop;
  oifname "lo" accept
  ct state established accept
 }
 chain forward { type filter hook forward priority -150; policy drop; }
}
`
	if got != want {
		t.Fatalf("buildOffFilterRules() =\n%s\nwant:\n%s", got, want)
	}
}

func TestInstallOffFilterReturnsNftFailure(t *testing.T) {
	nftPath, err := exec.LookPath("false")
	if err != nil {
		t.Skip("false executable is unavailable")
	}
	err = installOffFilter(nftPath, []string{"80"}, nil)
	if err == nil || !strings.Contains(err.Error(), "nft -f - failed") {
		t.Fatalf("installOffFilter() error = %v, want fail-closed nft error", err)
	}
}
