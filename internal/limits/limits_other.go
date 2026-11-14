//go:build !linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package limits

import (
	"fmt"

	"github.com/arwos/isobox/internal/profile"
)

// CheckHost is unavailable outside Linux.
func CheckHost(request profile.ResourceLimits) (string, error) {
	if Any(request) {
		return "", fmt.Errorf("profile resource limits are supported only on Linux")
	}
	return "", nil
}

// VerifyCgroupPID is unavailable outside Linux.
func VerifyCgroupPID(pid int, request profile.ResourceLimits) error {
	if NeedsCgroup(request) {
		return fmt.Errorf("cgroup resource limits are supported only on Linux")
	}
	return nil
}

// ApplyOpenFilesPID is unavailable outside Linux.
func ApplyOpenFilesPID(pid int, requested uint64) error {
	if requested != 0 {
		return fmt.Errorf("open-file limits are supported only on Linux")
	}
	return nil
}
