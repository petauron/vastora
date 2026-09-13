package agent

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
)

// Archival is idempotent by task identity and digest. A temporary Center
// outage must not permanently kill the task receiver while heartbeats survive.
// Invalid evidence/acknowledgements still stop closed; no business task is replayed.
func (c Client) transferLegacyReceiptsBeforeTasks(ctx context.Context, store *Store, report func(error)) bool {
	for {
		err := c.TransferLegacyReceipts(ctx, store)
		if err == nil {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		report(err)
		var response *centerResponseError
		var network net.Error
		retryable := errors.As(err, &network) && (network.Timeout() || network.Temporary())
		if errors.As(err, &response) {
			retryable = response.status == http.StatusTooManyRequests || response.status == http.StatusBadGateway || response.status == http.StatusServiceUnavailable || response.status == http.StatusGatewayTimeout
		}
		if !retryable || !waitForTaskRetry(ctx) {
			return false
		}
	}
}

// TransferLegacyReceipts transfers unresolved one-time cutover evidence, not
// acknowledged history. No application effect is replayed. Unknown results
// remain fenced in Center before this Agent can receive a new task.
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
