//go:build !linux

package proxy

import "fmt"

func Add(string, string, AuthMode, string, string) error {
	return fmt.Errorf("proxy credential storage is supported only on Linux")
}

func List() ([]Info, error) {
	return nil, fmt.Errorf("proxy credential storage is supported only on Linux")
}

func Inspect(string) (Info, error) {
	return Info{}, fmt.Errorf("proxy credential storage is supported only on Linux")
}

func Remove(string) error {
	return fmt.Errorf("proxy credential storage is supported only on Linux")
}
