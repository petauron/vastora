package deployapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/petauron/vastora/internal/glivelaruntime"
)

// GlivelaProbeRequest contains a client credential. Transport it only over
// the permission-restricted deployment-helper socket; never log the payload.
type GlivelaProbeRequest struct {
	Client       glivelaruntime.AcceptanceClient `json:"client"`
	ExpectedExit string                          `json:"expectedExit"`
}

type GlivelaProber interface {
	VerifyGlivelaEntry(context.Context, GlivelaProbeRequest) error
}

func (client *Client) VerifyGlivelaEntry(ctx context.Context, input GlivelaProbeRequest) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return errors.New("center: encode proxy verification request")
	}
	body, err := client.request(ctx, http.MethodPost, "/v1/meridian/verify", payload)
	if err != nil {
		return err
	}
	var result struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(body, &result) != nil || result.Status != "verified" {
		return errors.New("center: deployment helper did not confirm authenticated proxy verification")
	}
	return nil
}
