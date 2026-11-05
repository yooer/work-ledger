//go:build windows

package main

import (
	"context"

	"golang.org/x/sys/windows/svc"

	"work-ledger/internal/config"
	"work-ledger/internal/ctl"
	"work-ledger/internal/server"
)

type winService struct{ cfg *config.Config }

func (w *winService) Execute(args []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, w.cfg) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			cancel()
			if err != nil {
				return true, 1
			}
			return false, 0
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				<-done
				return false, 0
			}
		}
	}
}

// runAsWindowsService 由服务控制管理器启动时以服务方式运行。
func runAsWindowsService(cfg *config.Config) (bool, error) {
	isSvc, err := svc.IsWindowsService()
	if err != nil || !isSvc {
		return false, nil
	}
	return true, svc.Run(ctl.ServiceName, &winService{cfg: cfg})
}
