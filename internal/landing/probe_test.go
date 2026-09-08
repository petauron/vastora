package landing

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestProbeRejectsInvalidExitResponses(t *testing.T) {
	for _, body := range []string{"", "ip=127.0.0.1", "ip=100.64.0.8", "ip=10.0.0.1", "ip=169.254.169.254", "ip=::1", "ip=2001:4860:4860::8888", "ip=192.0.2.1", "ip=1.1.1.1\nip=8.8.8.8", "ip=invalid"} {
		if _, err := traceExit([]byte(body)); err == nil {
			t.Fatalf("accepted invalid exit %q", body)
		}
	}
	if value, err := traceExit([]byte("fl=probe\nip=1.1.1.1\nloc=US\n")); err != nil || value != "1.1.1.1" {
		t.Fatalf("exit %q: %v", value, err)
	}
}

type socksExchange struct {
	*bytes.Reader
	written bytes.Buffer
}

func (e *socksExchange) Write(value []byte) (int, error) { return e.written.Write(value) }

func TestSOCKSAuthenticationRejectsDowngradeAndFailure(t *testing.T) {
	credentials := Credentials{Username: "probe-user-123456", Password: "probe-secret-123456"}
	for _, response := range [][]byte{{5, 0}, {5, 255}, {4, 2}, {5, 2, 1, 1}, {5, 2, 0, 0}, {5}, {5, 2, 1}} {
		exchange := &socksExchange{Reader: bytes.NewReader(response)}
		if err := authenticateSOCKS(exchange, credentials); err == nil {
			t.Fatalf("accepted authentication response %v", response)
		}
	}
	exchange := &socksExchange{Reader: bytes.NewReader([]byte{5, 2, 1, 0})}
	if err := authenticateSOCKS(exchange, credentials); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(exchange.written.Bytes(), []byte{5, 1, 2, 1, byte(len(credentials.Username))}) {
		t.Fatal("authentication request offered an unauthenticated method")
	}
}

func TestProbeRequiresActualUDPDataAndExactRelay(t *testing.T) {
	for _, scenario := range []string{"healthy", "wrong relay", "association only", "wrong question", "wrong ID", "private answer", "fragment", "wrong source"} {
		t.Run(scenario, func(t *testing.T) {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			endpoint := netip.MustParseAddrPort(listener.Addr().String())
			udp, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(endpoint))
			if err != nil {
				t.Fatal(err)
			}
			defer udp.Close()
			deadline := time.Now().Add(2 * time.Second)
			_ = udp.SetDeadline(deadline)
			done := make(chan struct{})
			go func() {
				defer close(done)
				connection, err := listener.Accept()
				if err != nil {
					return
				}
				defer connection.Close()
				_ = connection.SetDeadline(deadline)
				var greeting [3]byte
				if _, err := io.ReadFull(connection, greeting[:]); err != nil || greeting != [3]byte{5, 1, 2} {
					return
				}
				_, _ = connection.Write([]byte{5, 2})
				var auth [2]byte
				if _, err := io.ReadFull(connection, auth[:]); err != nil || auth[0] != 1 {
					return
				}
				user := make([]byte, int(auth[1])+1)
				if _, err := io.ReadFull(connection, user); err != nil {
					return
				}
				password := make([]byte, int(user[len(user)-1]))
				if _, err := io.ReadFull(connection, password); err != nil || string(user[:len(user)-1]) != "probe-user-123456" || string(password) != "probe-secret-123456" {
					return
				}
				_, _ = connection.Write([]byte{1, 0})
				var associate [10]byte
				if _, err := io.ReadFull(connection, associate[:]); err != nil || associate != [10]byte{5, 3, 0, 1} {
					return
				}
				relay := endpoint
				if scenario == "wrong relay" {
					relay = netip.MustParseAddrPort("127.0.0.2:1080")
				}
				ip := relay.Addr().As4()
				response := []byte{5, 0, 0, 1, ip[0], ip[1], ip[2], ip[3], 0, 0}
				binary.BigEndian.PutUint16(response[8:], relay.Port())
				_, _ = connection.Write(response)
				buffer := make([]byte, 4096)
				n, source, err := udp.ReadFromUDP(buffer)
				if err != nil || n <= 10 || scenario == "association only" {
					return
				}
				var message dnsmessage.Message
				if err := message.Unpack(buffer[10:n]); err != nil {
					return
				}
				message.Response, message.RecursionAvailable = true, true
				if scenario == "wrong question" {
					message.Questions[0].Name = dnsmessage.MustNewName("other.example.")
				}
				if scenario == "wrong ID" {
					message.ID++
				}
				answer := [4]byte{1, 1, 1, 1}
				if scenario == "private answer" {
					answer = [4]byte{169, 254, 169, 254}
				}
				message.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: message.Questions[0].Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60}, Body: &dnsmessage.AResource{A: answer}}}
				payload, err := message.Pack()
				if err != nil {
					return
				}
				if scenario == "fragment" {
					buffer[2] = 1
				}
				if scenario == "wrong source" {
					buffer[4] = 8
				}
				_, _ = udp.WriteToUDP(append(buffer[:10:10], payload...), source)
				_, _ = io.Copy(io.Discard, connection)
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			probe := Probe{Credentials: Credentials{Username: "probe-user-123456", Password: "probe-secret-123456"}}
			err = probe.udp(ctx, endpoint.String())
			if (err == nil) != (scenario == "healthy") {
				t.Fatalf("UDP exchange: %v", err)
			}
			_ = listener.Close()
			_ = udp.Close()
			<-done
		})
	}
}
