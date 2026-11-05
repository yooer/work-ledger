//go:build !linux && !windows

package ctl

import "errors"

var errUnsupported = errors.New("当前操作系统不支持服务管理，请手动运行 work-ledger serve")

func serviceState() string                        { return "不支持" }
func controlService(action string) error          { return errUnsupported }
func requireAdmin() error                         { return nil }
func fixOwner(path, home string)                  {}
func InstallService(home, bin, user string) error { return errUnsupported }
func removeService() error                        { return errUnsupported }
func removeShortcut()                             {}
