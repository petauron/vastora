//go:build !linux

package nodesecurity

import (
	"context"
	"errors"
)

type Host struct{}

func NewHost() Host { return Host{} }
func (Host) Execute(context.Context, Task) (View, error) {
	return View{}, errors.New("security: Linux systemd hosts only")
}
func (Host) Watchdog(context.Context) error { return errors.New("security: Linux systemd hosts only") }
func (Host) VerifySSH(context.Context, string) error {
	return errors.New("security: Linux systemd hosts only")
}
func (Host) CheckPorts([]Rule) error { return nil }
func (Host) CheckRemoval() error     { return nil }
func (Host) CheckDynamicUDP() error  { return nil }
