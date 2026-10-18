//go:build linux

package nodesecurity

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPublicGuardPreservesPrivateAndRequiredNetworkTraffic(t *testing.T) {
	p := fixturePolicy()
	for _, v6 := range []bool{false, true} {
		rules := inputRules(p, v6)
		for _, must := range []string{"-A " + ufwBeforeInput(v6) + " -i eth0 -j ufw-vastora-security", "ESTABLISHED,RELATED -j RETURN", "--dport 443 -j RETURN", "--dport 41641 -j RETURN", "-j DROP"} {
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

func TestUFWEmptyRuleInventoryDoesNotMistakeHeaderForRule(t *testing.T) {
	const header = "Added user rules (see 'ufw status' for running firewall):\n"
	if !emptyUFWUserRules(header + "(None)\n") {
		t.Fatal("distribution empty inventory rejected")
	}
	for _, raw := range []string{"", header, header + "ufw allow 22/tcp", header + "(None)\nufw allow 443", "unknown output"} {
		if emptyUFWUserRules(raw) {
			t.Fatalf("accepted nonempty/unknown inventory %q", raw)
		}
	}
}

func TestInactiveUFWInputHooksMustBeKnownAndEmpty(t *testing.T) {
	for _, tool := range []string{"iptables", "ip6tables"} {
		name := ufwBeforeInput(tool == "ip6tables")
		h := Host{run: func(_ context.Context, got string, args ...string) (string, error) {
			if got != tool || strings.Join(args, " ") != "-S "+name {
				t.Fatalf("unexpected inspection %s %v", got, args)
			}
			return "-N " + name, nil
		}}
		if !h.emptyInactiveUFWInput(context.Background(), tool, "-A INPUT -j "+name) {
			t.Fatal("empty distribution hook rejected")
		}
		if h.emptyInactiveUFWInput(context.Background(), tool, "-A INPUT -j administrator") {
			t.Fatal("unowned chain accepted")
		}
		h.run = func(context.Context, string, ...string) (string, error) {
			return "-N " + name + "\n-A " + name + " -j ACCEPT", nil
		}
		if h.emptyInactiveUFWInput(context.Background(), tool, "-A INPUT -j "+name) {
			t.Fatal("nonempty chain accepted")
		}
	}
}

func TestRollbackRemovesOnlyOwnedInputGuard(t *testing.T) {
	calls := []string{}
	h := Host{run: func(_ context.Context, tool string, args ...string) (string, error) {
		calls = append(calls, tool+" "+strings.Join(args, " "))
		if strings.Join(args, " ") == "-S" {
			return "-N ufw-vastora-security\n-A ufw-before-input -i eth0 -j ufw-vastora-security", nil
		}
		return "", nil
	}}
	if err := h.removeInputGuard(context.Background(), "iptables", fixturePolicy()); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(calls, "\n")
	for _, want := range []string{"-D ufw-before-input -i eth0 -j ufw-vastora-security", "-F ufw-vastora-security", "-X ufw-vastora-security"} {
		if !strings.Contains(got, want) {
			t.Fatal("missing owned cleanup", want)
		}
	}
	if strings.Contains(got, "-F INPUT") || strings.Contains(got, "-F ufw-before-input") {
		t.Fatal("shared chain altered")
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
	v6 := strings.ReplaceAll(string(base), "ufw-before-", "ufw6-before-")
	if got, err := integrateBefore([]byte(v6), fixturePolicy(), true); err != nil || !strings.Contains(string(got), "-A ufw6-before-input -i eth0") {
		t.Fatalf("distribution IPv6 chain not supported: %v", err)
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

func TestSSHReceiptBindsActualTCPConnectionNotANewMultiplexedChild(t *testing.T) {
	now := time.Now()
	peer := []string{"203.0.113.10", "55000", "192.0.2.10", "2222"}
	line := `ESTAB 0 0 192.0.2.10:2222 203.0.113.10:55000 users:(("sshd",pid=100,fd=4),("sshd-session",pid=200,fd=4))`
	if freshSSHSocket(line, peer, map[int]time.Time{100: now.Add(-time.Minute), 200: now.Add(2 * time.Second)}, now) {
		t.Fatal("old TCP session accepted through a fresh multiplexed child")
	}
	if !freshSSHSocket(line, peer, map[int]time.Time{100: now.Add(2 * time.Second), 200: now.Add(3 * time.Second)}, now) {
		t.Fatal("fresh TCP socket rejected")
	}
	mappedLine := strings.ReplaceAll(strings.ReplaceAll(line, "192.0.2.10:2222", "[::ffff:192.0.2.10]:2222"), "203.0.113.10:55000", "[::ffff:203.0.113.10]:55000")
	if !freshSSHSocket(mappedLine, peer, map[int]time.Time{100: now.Add(2 * time.Second), 200: now.Add(3 * time.Second)}, now) {
		t.Fatal("dual-stack sshd mapped socket display rejected")
	}
	if freshSSHSocket(line, peer, map[int]time.Time{300: now.Add(2 * time.Second)}, now) {
		t.Fatal("unrelated process accepted")
	}
	other := []string{"203.0.113.11", "55000", "192.0.2.10", "2222"}
	if freshSSHSocket(line, other, map[int]time.Time{100: now.Add(2 * time.Second)}, now) {
		t.Fatal("different source socket accepted")
	}
}
