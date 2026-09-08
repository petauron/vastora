package deployapi

import (
	"context"
	"encoding/json"
	"github.com/petauron/vastora/internal/landing"
	"net/http"
)

type LandingPolicyRequest struct {
	Revision uint64               `json:"revision"`
	Rules    []landing.AccessRule `json:"rules"`
}
type LandingPolicyManager interface {
	ApplyLandingPolicy(context.Context, LandingPolicyRequest) error
}

func (c *Client) ApplyLandingPolicy(ctx context.Context, input LandingPolicyRequest) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	_, err = c.request(ctx, http.MethodPut, "/v1/headscale/landing-policy", payload)
	return err
}
