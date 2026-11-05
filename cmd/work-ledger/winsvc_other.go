//go:build !windows

package main

import "work-ledger/internal/config"

func runAsWindowsService(cfg *config.Config) (bool, error) { return false, nil }
