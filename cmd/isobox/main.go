/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package main

import (
	"os"

	"github.com/arwos/isobox/internal/cli"
	"github.com/arwos/isobox/internal/preflight"
	"github.com/arwos/isobox/internal/sandbox"
)

func main() {
	if code, handled := preflight.RunProbeHelper(os.Args[1:]); handled {
		os.Exit(code)
	}
	if code, handled := sandbox.RunOffGuardHelper(os.Args[1:]); handled {
		os.Exit(code)
	}
	if code, handled := sandbox.RunIPv4NetworkHelper(os.Args[1:]); handled {
		os.Exit(code)
	}
	if code, handled := sandbox.RunProxyTUNHelper(os.Args[1:]); handled {
		os.Exit(code)
	}
	os.Exit(cli.Execute(os.Args[1:], os.Stdout, os.Stderr))
}
