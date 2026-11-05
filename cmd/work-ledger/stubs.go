package main

import "work-ledger/internal/config"

func cmdCtl(args []string) error                           { return nil }
func cmdService(args []string) error                       { return nil }
func runAsWindowsService(cfg *config.Config) (bool, error) { return false, nil }
