//go:build linux

package sandbox

import "testing"

func TestShouldSignalProcessGroup(t *testing.T) {
	tests := []struct {
		name            string
		groupID         int
		supervisorGroup int
		want            bool
	}{
		{name: "missing group", groupID: 0, supervisorGroup: 42, want: false},
		{name: "same group as supervisor", groupID: 42, supervisorGroup: 42, want: false},
		{name: "separate child group", groupID: 84, supervisorGroup: 42, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldSignalProcessGroup(test.groupID, test.supervisorGroup); got != test.want {
				t.Fatalf("shouldSignalProcessGroup(%d, %d) = %t, want %t", test.groupID, test.supervisorGroup, got, test.want)
			}
		})
	}
}
