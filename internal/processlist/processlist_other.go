//go:build !linux

package processlist

import "fmt"

type Entry struct {
	PID     int
	AppID   string
	CWD     string
	Network string
}

type Usage struct {
	Entry
	MemoryBytes uint64
	CPUPercent  float64
}

func Register(int, string, string, string) (func(), error) {
	return func() {}, nil
}

func List() ([]Usage, error) {
	return nil, fmt.Errorf("process listing supports only Linux")
}

const (
	EnvAppID   = "ISOBOX_RUN_APP_ID"
	EnvCWD     = "ISOBOX_RUN_CWD"
	EnvNetwork = "ISOBOX_RUN_NETWORK"
)
