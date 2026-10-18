//go:build linux

package nodesecurity

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPublicGuardPreservesPrivateAndRequiredNetworkTraffic(t *testing.T) {
	p := fixturePolicy()
	for _, v6 := range []bool{false, true} {
		rules := inputRules(p, v6)
		for _, must := range []string{"-A ufw-before-input -i eth0 -j ufw-vastora-security", "ESTABLISHED,RELATED -j RETURN", "--dport 443 -j RETURN", "--dport 41641 -j RETURN", "-j DROP"} {
			if !strings.Contains(rules, must) {
				t.Fatalf("missing %q", must)
			}
		}
		if strings.Contains(rules, "-i tailscale0") || strings.Contains(rules, "-j ACCEPT") || strings.Contains(rules, "FORWARD") || strings.Contains(rules, "--dport 1080") {
			t.Fatal("guard expands private or landing permissions")
		}
		if v6 {
			if !strings.Contains(rules, "ipv6-icmp") || !strings.Contains(rules, "--sport 547 --dport 546") || strings.Contains(rules, "203.0.113") {
				t.Fatal("IPv6 guard incomplete")
			}
		} else {
			if !strings.Contains(rules, "--sport 67 --dport 68") || strings.Contains(rules, "2001:db8") {
				t.Fatal("IPv4 guard incomplete")
			}
		}
		if !strings.HasSuffix(rules, "-A ufw-vastora-security -j DROP\n") {
			t.Fatal("public interface is not deny by default")
		}
	}
}

func TestIntegrateBeforeDoesNotReplaceLandingOrAdministratorRules(t *testing.T) {
	base := []byte("# original\n*filter\n:ufw-before-input - [0:0]\n:ufw-before-output - [0:0]\n-A ufw-before-input -i lo -j ACCEPT\nCOMMIT\n")
	got, err := integrateBefore(base, fixturePolicy(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "-A ufw-before-input -i lo -j ACCEPT\nCOMMIT") {
		t.Fatal("existing content overwritten")
	}
	if strings.Index(string(got), "-i eth0") > strings.Index(string(got), "-i lo") {
		t.Fatal("public guard placed after an allow rule")
	}
	if _, err = integrateBefore(got, fixturePolicy(), false); err == nil {
		t.Fatal("duplicate guard adopted")
	}
	if _, err = integrateBefore([]byte("*filter\nCOMMIT"), fixturePolicy(), false); err == nil {
		t.Fatal("unknown format accepted")
	}
}

func TestSSHOnlyJailAndManagementExemptions(t *testing.T) {
	raw := string(jail(fixturePolicy()))
	for _, must := range []string{"[DEFAULT]\nenabled = false", "[sshd]\nenabled = true", "backend = systemd", "banaction = nftables-multiport", "port = 2222", "maxretry = 5", "findtime = 10m", "bantime = 10m", "203.0.113.10/32"} {
		if !strings.Contains(raw, must) {
			t.Fatal("missing jail constraint", must)
		}
	}
	if strings.Contains(raw, "allports") || strings.Contains(raw, "100.64.0.0/10") || strings.Contains(raw, "X-Forwarded-For") {
		t.Fatal("overbroad bans or exemptions")
	}
}

func TestDockerUsesOnlyUserChainAndOriginalPublishedPort(t *testing.T) {
	calls := []string{}
	h := Host{run: func(_ context.Context, name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return "", nil
	}}
	if err := h.dockerRules(context.Background(), "iptables", fixturePolicy(), true); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "--ctorigdstport 443") || !strings.Contains(joined, "--ctstatus DNAT -j DROP") || !strings.Contains(joined, "-I DOCKER-USER 1 -j VASTORA-DOCKER") {
		t.Fatal("Docker published port guard missing")
	}
	for _, bad := range []string{"-F DOCKER", "-P FORWARD", "-I FORWARD", "--dport 1080", " -j ACCEPT"} {
		if strings.Contains(joined, bad) {
			t.Fatal("Docker isolation changed", bad)
		}
	}
}

func TestCommandOutputAndProcessEvidenceAreBounded(t *testing.T) {
	var b limitedOutput
	if _, err := b.Write(make([]byte, 128*1024+1)); err == nil {
		t.Fatal("oversized command output accepted")
	}
	if _, _, err := processStart("broken"); err == nil {
		t.Fatal("broken process accepted")
	}
	if !supportedDistro("ID=ubuntu\nVERSION_ID=\"24.04\"\n") || supportedDistro("ID=alpine\nVERSION_ID=3.22") {
		t.Fatal("unsupported distribution handling")
	}
	h := Host{run: func(context.Context, string, ...string) (string, error) { return "", errors.New("missing") }}
	if h.verifyRuntime(context.Background(), &checkpoint{View: View{Policy: fixturePolicy()}}) == nil {
		t.Fatal("missing runtime treated as protected")
	}
}
