package center

import (
	"context"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/networking"
)

func TestLegacyControllerMigrationRejectsRemovedExecutorBeforeChangingState(t *testing.T) {
	for _, archivedCatalog := range []bool{false, true} {
		t.Run(map[bool]string{false: "current-catalog", true: "archived-catalog"}[archivedCatalog], func(t *testing.T) {
			store := openOrchestrationStore(t)
			t.Cleanup(func() { _ = store.Close() })
			ctx := context.Background()
			node := enrollOrchestrationNode(t, store, "existing-controller", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.10", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.10", LANAddress: "10.0.0.10", EnabledKinds: []string{networking.KindLAN}})
			deployment := seedLegacyControllerDeployment(t, store, node, "10.0.0.10", "migration-api-token")
			if archivedCatalog {
				seedLegacyProxyManifest(t, store)
			}
			_, err := store.CreateThreeXUIControllerMigration(ctx, deployment.ApplicationID, ThreeXUIControllerMigrationInput{TargetApplicationID: "replacement-controller", Confirm: true})
			if err == nil || !strings.Contains(err.Error(), "use Meridian cutover") {
				t.Fatalf("removed executor allowed controller relocation: %v", err)
			}
			for _, table := range []string{"three_x_ui_migrations", "three_x_ui_backups", "application_commands"} {
				var count int
				if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("rejected migration changed %s: count=%d err=%v", table, count, err)
				}
			}
			var role, status, selected string
			if err := store.db.QueryRowContext(ctx, `SELECT role,status FROM applications WHERE id=?`, deployment.ApplicationID).Scan(&role, &status); err != nil {
				t.Fatal(err)
			}
			if err := store.db.QueryRowContext(ctx, `SELECT controller_application_id FROM three_x_ui_control_plane WHERE id=1`).Scan(&selected); err != nil {
				t.Fatal(err)
			}
			if role != threeXUIRoleMaster || status != "running" || selected != deployment.ApplicationID {
				t.Fatalf("rejected migration changed live ownership: role=%q status=%q selected=%q", role, status, selected)
			}
		})
	}
}
