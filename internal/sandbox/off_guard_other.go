//go:build !linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package sandbox

func RunOffGuardHelper([]string) (int, bool) {
	return 0, false
}

func RunIPv4NetworkHelper([]string) (int, bool) {
	return 0, false
}

func RunProxyTUNHelper([]string) (int, bool) {
	return 0, false
}
