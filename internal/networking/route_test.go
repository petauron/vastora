package networking

import (
	"errors"
	"net"
	"testing"
	"time"
)

type routeTestConnection struct {
	local net.Addr
}

func (connection routeTestConnection) Read([]byte) (int, error)         { return 0, errors.New("unused") }
func (connection routeTestConnection) Write([]byte) (int, error)        { return 0, errors.New("unused") }
func (connection routeTestConnection) Close() error                     { return nil }
func (connection routeTestConnection) LocalAddr() net.Addr              { return connection.local }
func (connection routeTestConnection) RemoteAddr() net.Addr             { return &net.UDPAddr{} }
func (connection routeTestConnection) SetDeadline(time.Time) error      { return nil }
func (connection routeTestConnection) SetReadDeadline(time.Time) error  { return nil }
func (connection routeTestConnection) SetWriteDeadline(time.Time) error { return nil }

func TestDefaultRouteAddressUsesRequestedDestination(t *testing.T) {
	routes := map[string]string{
		"203.0.113.10":  "10.0.0.10",
		"198.51.100.20": "192.168.1.20",
	}
	for destination, expected := range routes {
		address, err := defaultRouteAddress(destination, func(network, target string) (net.Conn, error) {
			if want := net.JoinHostPort(destination, "53"); network != "udp4" || target != want {
				t.Fatalf("route selection dial = %s %s, want udp4 %s", network, target, want)
			}
			return routeTestConnection{local: &net.UDPAddr{IP: net.ParseIP(expected)}}, nil
		})
		if err != nil || address != expected {
			t.Fatalf("route to %s selected %q: %v", destination, address, err)
		}
	}
}
