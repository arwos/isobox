//go:build !linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

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
