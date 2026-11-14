//go:build !linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package cli

import "fmt"

func readProxyCredentials() (string, string, error) {
	return "", "", fmt.Errorf("interactive SOCKS5 credentials are supported only on Linux")
}
