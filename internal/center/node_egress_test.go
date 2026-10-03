package center

import (
	"context"
	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/meridianruntime"
	"testing"
	"time"
)

func nodeEgressFixture(t *testing.T) (*Store, string) {
	t.Helper()
	s := openMeridianSharedEndpointSnapshotFixture(t)
	var id string
	if err := s.db.QueryRow(`SELECT node_id FROM applications WHERE id='snapshot-shared-app'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE agents SET capabilities_json=json_set(capabilities_json,'$.nativeEgress',json('true')),last_seen_at=? WHERE id=?`, s.now().UTC().Format(time.RFC3339Nano), id); err != nil {
		t.Fatal(err)
	}
	return s, id
}

func TestNodeEgressQueuesOriginalIdentityAndRequiresEvidence(t *testing.T) {
	for _, success := range []bool{false, true} {
		s, id := nodeEgressFixture(t)
		ctx := context.Background()
		before, err := s.NodeEgress(ctx, id)
		if err != nil || !before.Available || before.Policy != meridian.EgressAuto || before.Revision != 0 {
			t.Fatalf("initial=%#v err=%v", before, err)
		}
		command, err := s.SetNodeEgress(ctx, id, NodeEgressInput{Policy: meridian.EgressIPv6Only})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.SetNodeEgress(ctx, id, NodeEgressInput{Policy: meridian.EgressAuto}); err == nil {
			t.Fatal("concurrent policy accepted")
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		p, err := s.buildMeridianRuntimeTask(ctx, tx, sharedSnapshotEndpointID, id)
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		tx.Rollback()
		if p.task.NativeEgress != meridian.EgressIPv6Only || p.task.ImageReference != meridianruntime.EgressImage || len(p.task.EgressClients) != 1 {
			t.Fatalf("policy projection missing: %#v", p.task.NativeEgress)
		}
		c := p.task.EgressClients[0]
		if c.Reality.AdvertiseHost != "100.64.0.61" || c.Reality.AdvertisePort != 10443 || c.Reality.PrivateKey != "" || c.Material.Credential.Kind != meridian.NativeCredential {
			t.Fatal("probe changed identity or targeted a landing")
		}
		if _, err := c.Config(1080); err != nil {
			t.Fatal(err)
		}
		if _, err = s.db.Exec(`UPDATE application_commands SET state='running',attempt=1 WHERE id=?`, command); err != nil {
			t.Fatal(err)
		}
		result := meridianHealthResult(p, s.now(), true)
		if success {
			result.Egress = &meridianruntime.EgressObservation{Policy: p.task.NativeEgress, ConfigSHA256: p.task.Desired.ConfigSHA256, Exits: []string{"2001:4860:4860::8888"}, CheckedAt: s.now()}
		}
		completeMeridianHealthFixture(t, s, p, command, result)
		after, err := s.NodeEgress(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if success {
			if after.AppliedPolicy != meridian.EgressIPv6Only || after.Verified == nil || after.State != "ready" {
				t.Fatalf("not verified: %#v", after)
			}
			// Quota closure must still render without a remaining enabled client.
			if _, err = s.db.Exec(`UPDATE meridian_accounts SET enabled=0`); err != nil {
				t.Fatal(err)
			}
			tx, err = s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			next, err := s.buildMeridianRuntimeTask(ctx, tx, sharedSnapshotEndpointID, id)
			tx.Rollback()
			if err != nil || len(next.task.EgressClients) != 0 || next.task.NativeEgress != meridian.EgressIPv6Only {
				t.Fatalf("quota mutation lost policy: %v", err)
			}
		} else if after.State != "failed" || after.Verified != nil || after.AppliedPolicy != meridian.EgressAuto {
			t.Fatalf("unverified marked applied: %#v", after)
		}
	}
}

func TestNodeEgressRejectsUnsupportedAndMissingClientsAtomically(t *testing.T) {
	s, id := nodeEgressFixture(t)
	ctx := context.Background()
	if _, err := s.SetNodeEgress(ctx, id, NodeEgressInput{Policy: "prefer_ipv6"}); err == nil {
		t.Fatal("unsupported preference accepted")
	}
	if _, err := s.db.Exec(`UPDATE meridian_accounts SET enabled=0`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetNodeEgress(ctx, id, NodeEgressInput{Policy: meridian.EgressIPv4Only}); err == nil {
		t.Fatal("missing native verification identity accepted")
	}
	view, err := s.NodeEgress(ctx, id)
	if err != nil || view.Revision != 0 || view.State != "ready" {
		t.Fatalf("failed change leaked: %#v %v", view, err)
	}
	if _, err := s.db.Exec(`UPDATE agents SET capabilities_json='{}' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetNodeEgress(ctx, id, NodeEgressInput{Policy: meridian.EgressIPv4Only}); err == nil {
		t.Fatal("old Agent accepted")
	}
}
