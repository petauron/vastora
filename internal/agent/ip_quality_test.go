package agent

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/ipquality"
)

func TestReadIPQualityOutputBoundsReportWithoutCountingProgress(t *testing.T) {
	var stream bytes.Buffer
	if err := writeIPQualityTestFrame(&stream, stdcopy.Stderr, []byte(strings.Repeat("progress", ipquality.MaxReportBytes))); err != nil {
		t.Fatal(err)
	}
	if err := writeIPQualityTestFrame(&stream, stdcopy.Stdout, []byte("{\"Head\":{}}")); err != nil {
		t.Fatal(err)
	}
	value, err := readIPQualityOutput(&stream)
	if err != nil {
		t.Fatal(err)
	}
	if string(value) != `{"Head":{}}` {
		t.Fatalf("unexpected stdout: %q", value)
	}
}

func TestReadIPQualityOutputRejectsOversizedReport(t *testing.T) {
	var stream bytes.Buffer
	if err := writeIPQualityTestFrame(&stream, stdcopy.Stdout, []byte(strings.Repeat("x", ipquality.MaxReportBytes+1))); err != nil {
		t.Fatal(err)
	}
	if _, err := readIPQualityOutput(&stream); err == nil {
		t.Fatal("oversized report accepted")
	}
}

// Moby's stdcopy reader remains available, but its framed test writer is no
// longer exported. Encode only the two frames needed by these reader tests.
func writeIPQualityTestFrame(stream *bytes.Buffer, kind stdcopy.StdType, payload []byte) error {
	header := [8]byte{byte(kind)}
	binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
	if _, err := stream.Write(header[:]); err != nil {
		return err
	}
	_, err := stream.Write(payload)
	return err
}

type fakeIPQualityCleanupEngine struct {
	removeErr  error
	inspectErr []error
	inspected  int
}

func (e *fakeIPQualityCleanupEngine) ContainerRemove(context.Context, string, client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	return client.ContainerRemoveResult{}, e.removeErr
}

func (e *fakeIPQualityCleanupEngine) ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	index := e.inspected
	e.inspected++
	if index < len(e.inspectErr) {
		return client.ContainerInspectResult{}, e.inspectErr[index]
	}
	return client.ContainerInspectResult{}, nil
}

func TestIPQualityContainerIsBoundedAndDoesNotMountHost(t *testing.T) {
	options := ipQualityContainerOptions(ipquality.Task{Address: "203.0.113.8", BindAddress: "10.0.0.8"})
	host := options.HostConfig
	if host.AutoRemove || !host.ReadonlyRootfs || host.Privileged || len(host.Binds) != 0 || len(host.Mounts) != 0 || host.Memory != 128*1024*1024 || host.MemorySwap != host.Memory || host.PidsLimit == nil || *host.PidsLimit > 96 {
		t.Fatalf("unsafe diagnostic container: %#v", host)
	}
	if options.Config.User != "65534:65534" || options.Config.Cmd[0] != "10.0.0.8" || options.Config.Cmd[1] != "-4" || !strings.Contains(options.Config.Image, "@sha256:") {
		t.Fatal("container identity or address not pinned")
	}
	if options.Config.Labels["io.vastora.application"] != "meridian" {
		t.Fatal("IP diagnostics lost Meridian ownership")
	}
	ipv6 := ipQualityContainerOptions(ipquality.Task{Address: "2001:db8::8", BindAddress: "2001:db8::8"})
	if ipv6.Config.Cmd[0] != "2001:db8::8" || ipv6.Config.Cmd[1] != "-6" {
		t.Fatal("IPv6 diagnostic lost its exact source address or family")
	}
	for _, required := range []string{"sha256sum -c", "-E -n -p -f -j", "bash /tmp/check.sh -i \"$1\" \"$2\"", `s/\$tmpcurlarg/\$CurlARG/g`, "/^check_mail$/d", "/^countRunTimes$/d", "check_dnsbl", "ad222ab16778be2a13a174cd1acbd69fb4cac6b7", "https://my.ippure.com/v1/info", "--interface \"$1\" --noproxy '*'"} {
		if !strings.Contains(ipQualityScript, required) {
			t.Fatalf("missing runner restriction %q", required)
		}
	}
	if options.Config.Entrypoint[0] != "timeout" || options.Config.Entrypoint[3] != "210" {
		t.Fatal("container lost independent lifetime limit")
	}
}

func TestIPQualityCleanupAcceptsRemovedOrAbsentContainer(t *testing.T) {
	for _, removeErr := range []error{nil, errdefs.ErrNotFound} {
		engine := &fakeIPQualityCleanupEngine{removeErr: removeErr}
		if err := cleanupIPQualityContainer(context.Background(), engine, "diagnostic"); err != nil || engine.inspected != 0 {
			t.Fatalf("remove error %v was not accepted: inspected=%d err=%v", removeErr, engine.inspected, err)
		}
	}
}

func TestIPQualityCleanupConfirmsConflictingRemovalCompleted(t *testing.T) {
	engine := &fakeIPQualityCleanupEngine{removeErr: errdefs.ErrConflict, inspectErr: []error{nil, errdefs.ErrNotFound}}
	if err := cleanupIPQualityContainer(context.Background(), engine, "diagnostic"); err != nil || engine.inspected != 2 {
		t.Fatalf("concurrent removal was not confirmed: inspected=%d err=%v", engine.inspected, err)
	}
}

func TestIPQualityCleanupFailsClosedWhenContainerRemains(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	engine := &fakeIPQualityCleanupEngine{removeErr: errdefs.ErrConflict}
	if err := cleanupIPQualityContainer(ctx, engine, "diagnostic"); !errors.Is(err, errIPQualityCleanupUnconfirmed) || engine.inspected == 0 {
		t.Fatalf("unconfirmed cleanup did not fail closed: inspected=%d err=%v", engine.inspected, err)
	}
}
