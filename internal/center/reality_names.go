package center

import (
	"context"
	"strconv"
	"strings"
	"time"
)

func realityBaseName(displayName, code string) string {
	code, ok := regionCode(code)
	if !ok {
		return strings.TrimSpace(displayName)
	}
	prefixes := []string{
		regionPrefix(code),
		regionFlag(code) + " " + code + " · ",
		regionFlag(code) + " " + code + " ",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(displayName, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(displayName, prefix))
		}
	}
	return strings.TrimSpace(displayName)
}

func (s *Store) RunRealityNameReconciliation(ctx context.Context, interval time.Duration, report func(error)) {
	if interval < 10*time.Second {
		interval = time.Minute
	}
	run := func() {
		if err := s.reconcileRealityDisplayName(ctx); err != nil && report != nil {
			report(err)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func (s *Store) reconcileRealityDisplayName(ctx context.Context) error {
	var active int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE state IN ('pending', 'running') OR reconciliation_required = 1`).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, application_id, name, region_code, display_name FROM services
		WHERE app_protocol = 'vless/tcp/reality' AND status <> 'stopped' AND region_code <> '' ORDER BY site_id, id`)
	if err != nil {
		return err
	}
	type candidate struct{ id, applicationID, serviceName, code, displayName string }
	values := []candidate{}
	for rows.Next() {
		var value candidate
		if err := rows.Scan(&value.id, &value.applicationID, &value.serviceName, &value.code, &value.displayName); err != nil {
			rows.Close()
			return err
		}
		values = append(values, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, value := range values {
		name := realityBaseName(value.displayName, value.code)
		_, _, desired, err := composeRealityDisplayName(value.code, name)
		if err != nil || desired == value.displayName {
			continue
		}
		inboundID, parseErr := strconv.Atoi(strings.TrimPrefix(value.serviceName, "inbound-"))
		if parseErr != nil || inboundID < 1 {
			continue
		}
		var recentFailures int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands
			WHERE application_id = ? AND kind = ? AND state = 'failed'
			AND COALESCE(json_extract(input_json, '$.inboundId'), 0) = ?
			AND COALESCE(json_extract(input_json, '$.displayName'), '') = ? AND updated_at >= ?`,
			value.applicationID, realityRenameCommandKind, inboundID, desired, s.now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)).Scan(&recentFailures); err != nil {
			return err
		}
		if recentFailures != 0 {
			continue
		}
		_, err = s.CreateRealityRenameCommand(ctx, RealityRenameCommandInput{ServiceID: value.id, RegionCode: value.code, Name: name})
		return err
	}
	return nil
}
