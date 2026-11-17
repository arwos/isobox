//go:build !linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package networkmanager

import (
	"fmt"
	"net/netip"
	"os"

	"github.com/arwos/isobox/internal/profile"
)

type Network struct {
	Name          string
	Subnet        netip.Prefix
	Gateway       netip.Addr
	Bridge        string
	State         string
	ActiveMembers int
}

type MemberAttachment struct {
	Group        string
	Interface    string
	Address      string
	Prefix       int
	NamespacePID int
}

type MemberTUNInfo struct {
	InterfaceName       string
	Subnet              string
	GuestAddress        string
	ResolverEndpoint    string
	HostGatewayEndpoint string
	BackendNamespacePID int
}

type MemberTunnel interface {
	Done() <-chan struct{}
	Err() error
	Close() error
}

type MemberTunnelStarter func(*os.File, MemberTUNInfo) (MemberTunnel, error)

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

func RunJoined([]string, []string, string, []string, []string, *os.File, *os.File, *os.File, MemberTunnelStarter) (int, error) {
	return 0, fmt.Errorf("named networks are supported only on Linux")
}

func SubmitMemberTUN(*os.File, MemberTUNInfo) error {
	return fmt.Errorf("named networks are supported only on Linux")
}

func SubmitMemberBackend(MemberTUNInfo) error {
	return fmt.Errorf("named networks are supported only on Linux")
}

func MemberRuntime() (bool, []MemberAttachment, int, int, error) {
	if os.Getenv("ISOBOX_INTERNAL_GROUP_MEMBER") != "" {
		return true, nil, 0, 0, fmt.Errorf("grouped networks are supported only on Linux")
	}
	return false, nil, 0, 0, nil
}

type MemberDNSConfig struct {
	Mode             profile.NetworkMode
	Upstreams        []string
	ResolverEndpoint string
}

func StartMemberDNS(MemberDNSConfig) (func(), error) {
	return nil, fmt.Errorf("named networks are supported only on Linux")
}
