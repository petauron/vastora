package agent

// Partial indexes covering pending completions, unresolved application work,
// and acknowledged/update receipt cleanup in the baseline Agent schema.
const taskReceiptIndexesSQL = `
	CREATE INDEX task_receipts_pending_completion ON task_receipts(updated_at, task_id)
		WHERE state IN ('completed', 'reconciliation_required');
	CREATE INDEX task_receipts_unresolved_application ON task_receipts(created_at, task_id)
		WHERE state IN ('processing', 'reconciliation_required', 'reconciliation_acknowledged') AND task_kind IN ('application.apply', 'legacy');
	CREATE INDEX task_receipts_expiry ON task_receipts(updated_at, task_id)
		WHERE state = 'acknowledged' OR (task_kind = 'agent.update' AND state = 'processing');
`
