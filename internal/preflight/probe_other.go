//go:build !linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package preflight

import "fmt"

func probeUserNamespace() error {
	return fmt.Errorf("user namespace probes are supported only on Linux")
}

func probeTUN() error {
	return fmt.Errorf("TUN probes are supported only on Linux")
}

func RunProbeHelper(args []string) (int, bool) {
	return 0, false
}
