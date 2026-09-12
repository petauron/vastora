package landing

// ControllerTask is encrypted in the existing Center-to-Agent task envelope.
// The credential is generated once, persisted encrypted, and reused on retry.
type ControllerTask struct {
	Grant           ClientGrant    `json:"grant"`
	Revision        uint64         `json:"revision"`
	Phase           string         `json:"phase"`
	ControllerID    string         `json:"controllerId"`
	InboundID       int            `json:"inboundId"`
	FixedUUID       string         `json:"fixedUuid,omitempty"`
	ConnectHostname string         `json:"connectHostname"`
	EntryName       string         `json:"entryName"`
	LandingName     string         `json:"landingName"`
	Mode            PublishingMode `json:"mode"`
}

type ControllerResult struct {
	GrantID           string `json:"grantId"`
	Revision          uint64 `json:"revision"`
	Phase             string `json:"phase"`
	BaseLink          string `json:"baseLink,omitempty"`
	FixedLink         string `json:"fixedLink,omitempty"`
	SubscriptionToken string `json:"subscriptionToken,omitempty"`
}

const ClientRuntimeGeneration = 1
const SubscriptionPort = 2097

type ClientRuntime struct {
	Generation int          `json:"generation"`
	Peer       PeerIdentity `json:"peer"`
}
