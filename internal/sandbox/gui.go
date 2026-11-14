/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package sandbox

const sandboxGUIRuntime = "/run/isobox"

type guiSetup struct {
	mounts      []profileMount
	environment map[string]string
	runtimeDir  bool
}
