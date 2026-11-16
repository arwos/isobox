//go:build !linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package networkmanager

import (
	"fmt"
	"net/netip"
)

type Network struct {
	Name          string
	Subnet        netip.Prefix
	Gateway       netip.Addr
	Bridge        string
	State         string
	ActiveMembers int
}

func Create(string, string) (Network, error) {
	return Network{}, fmt.Errorf("named networks are supported only on Linux")
}

func List() ([]Network, error) {
	return nil, fmt.Errorf("named networks are supported only on Linux")
}

func Remove(string) error {
	return fmt.Errorf("named networks are supported only on Linux")
}

func StartManager(string) error {
	return fmt.Errorf("named networks are supported only on Linux")
}

func RunManagerWorker(string) error {
	return fmt.Errorf("named networks are supported only on Linux")
}
