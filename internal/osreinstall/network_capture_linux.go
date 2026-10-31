//go:build linux

package osreinstall

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"
)

func readReviewedFile(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0022 != 0 || before.Size() > 64<<10 {
		return nil, errors.New("reinstall: network source requires review")
	}
	info, ok := before.Sys().(*syscall.Stat_t)
	if !ok || info.Uid != 0 {
		return nil, errors.New("reinstall: network source requires review")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, errors.New("reinstall: network source changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, before.Size()+1))
	if err != nil || int64(len(data)) != before.Size() {
		return nil, errors.New("reinstall: network source changed")
	}
	return data, nil
}

func managerActive(ctx context.Context, unit string) (bool, error) {
	cmd := exec.CommandContext(ctx, "systemctl", "is-active", unit)
	out := &limitedOutput{max: 1024}
	cmd.Stdout = out
	err := cmd.Run()
	state := strings.TrimSpace(out.buf.String())
	switch state {
	case "active":
		if err == nil {
			return true, nil
		}
	case "inactive", "unknown":
		if ctx.Err() == nil {
			return false, nil
		}
	}
	return false, errors.New("reinstall: network manager state requires review")
}

// CaptureStaticNetworkPlan reads declared policies and verifies them against
// every physical interface. Dynamic leases remain dynamic on the target.
func CaptureStaticNetworkPlan(parent context.Context, reviewed Inspection) (StaticNetworkPlan, error) {
	ctx, cancel := context.WithTimeout(parent, 25*time.Second)
	defer cancel()
	current, err := InspectHost(ctx)
	if err != nil || RecheckObservedHost(reviewed, current) != nil {
		return StaticNetworkPlan{}, networkError()
	}
	for _, v := range current.Limitations {
		if v != "network_configuration_requires_review" {
			return StaticNetworkPlan{}, networkError()
		}
	}
	if current.Architecture != "amd64" || current.BootMode != "bios" {
		return StaticNetworkPlan{}, networkError()
	}
	resolver, err := readReviewedFile("/etc/resolv.conf")
	if err != nil {
		return StaticNetworkPlan{}, err
	}
	kind, paths, sources, err := readNetworkSources()
	if err != nil || kind != current.NetworkCandidate || verifyManagers(ctx, kind) != nil {
		return StaticNetworkPlan{}, networkError()
	}
	plan, err := parseNetworkSources(kind, sources, resolver, current.Network)
	if err != nil {
		return plan, err
	}
	plans := planNICs(plan)
	for i := range plans {
		p := &plans[i]
		ra, err := os.ReadFile(filepath.Join("/proc/sys/net/ipv6/conf", p.Interface, "accept_ra"))
		if err != nil {
			return plan, networkError()
		}
		v := strings.TrimSpace(string(ra))
		if v != "0" && v != "1" && v != "2" || p.IPv6AcceptRA != "" && p.IPv6AcceptRA != v {
			return plan, networkError()
		}
		p.IPv6AcceptRA = v
		ac, e := os.ReadFile(filepath.Join("/proc/sys/net/ipv6/conf", p.Interface, "autoconf"))
		if e != nil {
			return plan, networkError()
		}
		value := strings.TrimSpace(string(ac))
		if value != "0" && value != "1" || p.IPv6Autoconf != "" && p.IPv6Autoconf != value {
			return plan, networkError()
		}
		p.IPv6Autoconf = value
		for key, want := range map[string]string{"disable_ipv6": "0"} {
			b, e := os.ReadFile(filepath.Join("/proc/sys/net/ipv6/conf", p.Interface, key))
			if e != nil || strings.TrimSpace(string(b)) != want {
				return plan, networkError()
			}
		}
	}
	plan = plans[0]
	plan.Additional = plans[1:]
	for _, family := range []string{"-4", "-6"} {
		rules, e := readCommand(ctx, "ip", "-j", family, "rule", "show")
		if e != nil {
			return plan, e
		}
		routes, e := readCommand(ctx, "ip", "-j", family, "route", "show", "table", "all")
		if e != nil || ValidatePlanRouting(rules, routes, family, plan, current.Network) != nil {
			return plan, networkError()
		}
	}
	after, err := InspectHost(ctx)
	if err != nil || RecheckObservedHost(reviewed, after) != nil {
		return plan, networkError()
	}
	lastKind, lastPaths, lastSources, err := readNetworkSources()
	if err != nil || kind != lastKind || !reflect.DeepEqual(paths, lastPaths) || !reflect.DeepEqual(sources, lastSources) {
		return plan, networkError()
	}
	lastResolver, err := readReviewedFile("/etc/resolv.conf")
	if err != nil || !bytes.Equal(resolver, lastResolver) {
		return plan, networkError()
	}
	for _, p := range planNICs(plan) {
		ra, e := os.ReadFile(filepath.Join("/proc/sys/net/ipv6/conf", p.Interface, "accept_ra"))
		if e != nil || strings.TrimSpace(string(ra)) != p.IPv6AcceptRA {
			return plan, networkError()
		}
		ac, e := os.ReadFile(filepath.Join("/proc/sys/net/ipv6/conf", p.Interface, "autoconf"))
		if e != nil || strings.TrimSpace(string(ac)) != p.IPv6Autoconf {
			return plan, networkError()
		}
	}
	return plan, nil
}
