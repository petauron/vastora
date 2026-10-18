//go:build linux

package nodesecurity

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// No privileged filesystem or network changes: these tests exercise the
// confirmation refusal path and an idempotent, already-verified confirmation.
func confirmationHost(p Policy, fail string) Host {
	return Host{now: time.Now, run: func(_ context.Context, tool string, args ...string) (string, error) {
		command := tool + " " + strings.Join(args, " ")
		if command == fail {
			return "", errors.New("fixture: runtime drift")
		}
		if command == "ufw status" {
			return "Status: active", nil
		}
		if command == "fail2ban-client status" {
			return "Jail list: sshd", nil
		}
		if (tool == "iptables" || tool == "ip6tables") && len(args) == 2 && args[0] == "-S" {
			name := args[1]
			var lines []string
			lines = append(lines, "-N "+name)
			if name == "DOCKER-USER" {
				lines = append(lines, "-A DOCKER-USER -j VASTORA-DOCKER")
			}
			if name == "VASTORA-DOCKER" {
				for _, rule := range dockerGuardRules(p) {
					lines = append(lines, "-A "+name+" "+strings.Join(rule, " "))
				}
			}
			for _, line := range strings.Split(strings.TrimSpace(inputRules(p, tool == "ip6tables")), "\n") {
				if strings.HasPrefix(line, "-A "+name+" ") {
					lines = append(lines, line)
				}
			}
			return strings.Join(lines, "\n"), nil
		}
		return "", nil
	}}
}

func TestConfirmationCannotCancelRecoveryAfterRuntimeDrift(t *testing.T) {
	p := fixturePolicy()
	for _, fail := range []string{
		"ufw status",
		"iptables -C " + chain + " -j DROP",
		"ip6tables -C " + chain + " -j DROP",
		"fail2ban-client status sshd",
		"iptables -S DOCKER-USER",
		"ip6tables -S DOCKER-USER",
		"iptables -C FORWARD -j DOCKER-USER",
		"ip6tables -C FORWARD -j DOCKER-USER",
		"iptables -C DOCKER-USER -j VASTORA-DOCKER",
		"iptables -C VASTORA-DOCKER " + strings.Join(dockerGuardRules(p)[0], " "),
	} {
		for _, state := range []string{"pending_confirmation", "verified"} {
			t.Run(state+"/"+fail, func(t *testing.T) {
				s := &checkpoint{View: View{State: state, Policy: p, Deadline: "keep-recovery-deadline"}, SSHVerified: true, Docker: true}
				if _, err := confirmationHost(p, fail).confirm(context.Background(), s); err == nil {
					t.Fatal("confirmation accepted stale runtime evidence")
				}
				if s.View.State != state || s.View.Deadline != "keep-recovery-deadline" {
					t.Fatal("failed confirmation changed recovery state")
				}
			})
		}
	}
}

func TestConfirmationRefusesEmptyOrBypassedDockerGuards(t *testing.T) {
	p := fixturePolicy()
	for _, change := range []string{"empty-child", "earlier-parent-accept", "reordered-child"} {
		h := confirmationHost(p, "")
		original := h.run
		h.run = func(ctx context.Context, tool string, args ...string) (string, error) {
			if tool == "iptables" && strings.Join(args, " ") == "-S VASTORA-DOCKER" && change == "empty-child" {
				return "-N VASTORA-DOCKER", nil
			}
			if tool == "iptables" && strings.Join(args, " ") == "-S VASTORA-DOCKER" && change == "reordered-child" {
				rules := dockerGuardRules(p)
				rules[0], rules[len(rules)-1] = rules[len(rules)-1], rules[0]
				lines := []string{"-N VASTORA-DOCKER"}
				for _, rule := range rules {
					lines = append(lines, "-A VASTORA-DOCKER "+strings.Join(rule, " "))
				}
				return strings.Join(lines, "\n"), nil
			}
			if tool == "iptables" && strings.Join(args, " ") == "-S DOCKER-USER" && change == "earlier-parent-accept" {
				return "-N DOCKER-USER\n-A DOCKER-USER -j ACCEPT\n-A DOCKER-USER -j VASTORA-DOCKER", nil
			}
			return original(ctx, tool, args...)
		}
		s := &checkpoint{View: View{State: "verified", Policy: p}, Docker: true}
		if _, err := h.confirm(context.Background(), s); err == nil {
			t.Fatal("accepted changed Docker enforcement", change)
		}
	}
}

func TestVerifiedConfirmationReplayStillChecksRuntime(t *testing.T) {
	p := fixturePolicy()
	s := &checkpoint{View: View{State: "verified", Policy: p}, Docker: true}
	v, err := confirmationHost(p, "").confirm(context.Background(), s)
	if err != nil || v.State != "verified" {
		t.Fatal("healthy verified replay rejected", err)
	}
}

func TestConfirmationCannotExpireDuringRuntimeVerification(t *testing.T) {
	p := fixturePolicy()
	deadline := time.Now().UTC().Add(time.Minute)
	s := &checkpoint{View: View{State: "pending_confirmation", Policy: p, Deadline: deadline.Format(time.RFC3339Nano)}, SSHVerified: true}
	h := confirmationHost(p, "")
	h.now = func() time.Time { return deadline }
	if _, err := h.confirm(context.Background(), s); err == nil {
		t.Fatal("accepted confirmation after its deadline")
	}
	if s.View.State != "pending_confirmation" || s.View.Deadline != deadline.Format(time.RFC3339Nano) {
		t.Fatal("expired confirmation cancelled recovery")
	}
}

func TestConfirmationRequiresFreshSSHBeforeRuntimeChecks(t *testing.T) {
	h := Host{run: func(context.Context, string, ...string) (string, error) {
		t.Fatal("runtime consulted before SSH receipt validated")
		return "", nil
	}}
	if _, err := h.confirm(context.Background(), &checkpoint{View: View{State: "pending_confirmation"}}); err == nil {
		t.Fatal("confirmation accepted missing fresh SSH receipt")
	}
}
