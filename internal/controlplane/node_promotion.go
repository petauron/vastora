package controlplane

// GatewayPromotion is an operator-issued authorization bound to one existing
// machine. It grants only the normal worker/gateway purpose, not arbitrary roles.
type GatewayPromotion struct {
	ID      string `json:"id"`
	AgentID string `json:"agentId"`
	State   string `json:"state"`
}
