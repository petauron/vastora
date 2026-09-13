package agent

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
)

// TransferLegacyReceipts is one-time cutover, not a completion outbox. No
// application effect is replayed. Unknown results remain fenced in Center.
func (c Client) TransferLegacyReceipts(ctx context.Context, store *Store) error {
	for {
		item, digest, err := store.NextLegacyReceipt(ctx, "")
		if err != nil {
			return err
		}
		if item == nil {
			return nil
		}
		if c.executionSession == "" {
			return errors.New("agent: legacy evidence transfer requires a registered session")
		}
		connection, err := store.Connection(ctx)
		if err != nil {
			return err
		}
		input := controlplane.LegacyReceiptImport{SessionID: c.executionSession, Digest: digest, Receipt: *item}
		var ack struct {
			Archived bool   `json:"archived"`
			ID       string `json:"id"`
			Digest   string `json:"digest"`
		}
		requestContext, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = c.postLimit(requestContext, connection.CenterURL+"/api/v1/agents/"+url.PathEscape(connection.AgentID)+"/legacy-receipts", input, connection.Credential, connection.CAFingerprint, connection.CACertificatePEM, &ack, controlplane.LegacyReceiptMaxPayloadBytes)
		cancel()
		if err != nil {
			return err
		}
		if !ack.Archived || ack.ID != controlplane.LegacyReceiptArchiveID(connection.AgentID, item.TaskID, item.Attempt) || ack.Digest != digest {
			return errors.New("agent: Center did not confirm matching legacy evidence")
		}
		if err := store.RetireLegacyReceipt(ctx, item.TaskID, digest); err != nil {
			return err
		}
	}
}
