package meridianruntime

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"slices"
	"strings"

	"github.com/google/uuid"
)

const LegacyExportKind = "meridian.legacy.export"

// LegacyExportCommand is the only secret-bearing read from the superseded
// controller. It is accepted only while the explicit cutover is in its import
// phase and never becomes a steady-state compatibility path.
type LegacyExportCommand struct {
	ApplicationID string `json:"applicationId"`
}

func (c LegacyExportCommand) Validate() error {
	if strings.TrimSpace(c.ApplicationID) == "" || len(c.ApplicationID) > 128 {
		return errors.New("meridian runtime: invalid legacy export command")
	}
	return nil
}

type LegacyClient struct {
	Email             string         `json:"email"`
	UUID              string         `json:"uuid"`
	SubscriptionToken string         `json:"subscriptionToken"`
	Enabled           bool           `json:"enabled"`
	TotalBytes        int64          `json:"totalBytes"`
	UsedBytes         int64          `json:"usedBytes"`
	UsageBaseline     int64          `json:"usageBaseline"`
	UsageObserved     int64          `json:"usageObserved"`
	ExpiryTime        int64          `json:"expiryTime"`
	ResetDays         int            `json:"resetDays"`
	LimitIP           int            `json:"limitIp"`
	InboundIDs        []int          `json:"inboundIds"`
	HY2AuthByInbound  map[int]string `json:"hy2AuthByInbound,omitempty"`
}

type LegacyEndpoint struct {
	InboundID       int      `json:"inboundId"`
	RemoteNodeID    int      `json:"remoteNodeId,omitempty"`
	Tag             string   `json:"tag"`
	DisplayName     string   `json:"displayName"`
	Listen          string   `json:"listen"`
	Port            int      `json:"port"`
	Enabled         bool     `json:"enabled"`
	TotalBytes      int64    `json:"totalBytes"`
	UsedBytes       int64    `json:"usedBytes"`
	Target          string   `json:"target"`
	ServerNames     []string `json:"serverNames"`
	PrivateKey      string   `json:"privateKey"`
	PublicKey       string   `json:"publicKey"`
	ShortIDs        []string `json:"shortIds"`
	Fingerprint     string   `json:"fingerprint"`
	TrafficResetDay int      `json:"trafficResetDay"`
	VLESSEnabled    bool     `json:"vlessEnabled"`
	HY2Configured   bool     `json:"hy2Configured"`
	HY2Enabled      bool     `json:"hy2Enabled"`
	HY2Tag          string   `json:"hy2Tag,omitempty"`
	HY2ServerName   string   `json:"hy2ServerName,omitempty"`
	HY2Certificate  string   `json:"hy2Certificate,omitempty"`
	HY2PrivateKey   string   `json:"hy2PrivateKey,omitempty"`
}

type LegacyRoute struct {
	ID                 string `json:"id"`
	ParentIdentityHash string `json:"parentIdentityHash"`
	InboundID          int    `json:"inboundId"`
	InboundTag         string `json:"inboundTag"`
	BaseUser           string `json:"baseUser"`
	FixedUser          string `json:"fixedUser"`
	FixedUUID          string `json:"fixedUuid"`
	EgressNodeID       string `json:"egressNodeId"`
	Enabled            bool   `json:"enabled"`
	HideNative         bool   `json:"hideNative"`
	Revision           uint64 `json:"revision"`
	UsageBaseline      int64  `json:"usageBaseline"`
	UsageObserved      int64  `json:"usageObserved"`
}

type LegacyExport struct {
	ControllerApplicationID string           `json:"controllerApplicationId"`
	Clients                 []LegacyClient   `json:"clients"`
	Endpoints               []LegacyEndpoint `json:"endpoints"`
	Routes                  []LegacyRoute    `json:"routes"`
}

type LegacyExportResult struct {
	Export LegacyExport `json:"export"`
}

func (r LegacyExportResult) Validate(command LegacyExportCommand) error {
	if command.Validate() != nil || r.Export.ControllerApplicationID != command.ApplicationID || len(r.Export.Clients) > 10000 || len(r.Export.Endpoints) == 0 || len(r.Export.Endpoints) > 1000 || len(r.Export.Routes) > 100000 {
		return errors.New("meridian runtime: invalid legacy export")
	}
	clientsByIdentity := make(map[string]LegacyClient, len(r.Export.Clients))
	emails, tokens := map[string]bool{}, map[string]bool{}
	for index := range r.Export.Clients {
		client := &r.Export.Clients[index]
		client.Email, client.UUID, client.SubscriptionToken = strings.TrimSpace(client.Email), strings.ToLower(strings.TrimSpace(client.UUID)), strings.TrimSpace(client.SubscriptionToken)
		slices.Sort(client.InboundIDs)
		client.InboundIDs = slices.Compact(client.InboundIDs)
		if client.Email == "" || len(client.Email) > 256 || uuid.Validate(client.UUID) != nil || !validLegacyToken(client.SubscriptionToken) || client.TotalBytes < 0 || client.UsedBytes < 0 || client.UsageBaseline < 0 || client.UsageObserved < client.UsageBaseline || client.ExpiryTime < 0 || client.ResetDays < 0 || client.ResetDays > 3650 || client.LimitIP < 0 || len(client.InboundIDs) == 0 || emails[client.Email] || tokens[client.SubscriptionToken] {
			return errors.New("meridian runtime: invalid legacy client")
		}
		for inboundIndex, inboundID := range client.InboundIDs {
			if inboundID < 1 || inboundIndex > 0 && client.InboundIDs[inboundIndex-1] == inboundID {
				return errors.New("meridian runtime: invalid legacy client attachment")
			}
		}
		identity := LegacyIdentityFingerprint(client.UUID)
		if _, exists := clientsByIdentity[identity]; exists {
			return errors.New("meridian runtime: duplicate legacy client identity")
		}
		clientsByIdentity[identity], emails[client.Email], tokens[client.SubscriptionToken] = *client, true, true
	}
	endpoints := make(map[int]LegacyEndpoint, len(r.Export.Endpoints))
	for index := range r.Export.Endpoints {
		endpoint := &r.Export.Endpoints[index]
		endpoint.Tag, endpoint.DisplayName, endpoint.Listen, endpoint.Target = strings.TrimSpace(endpoint.Tag), strings.TrimSpace(endpoint.DisplayName), strings.TrimSpace(endpoint.Listen), strings.TrimSpace(endpoint.Target)
		endpoint.PublicKey, endpoint.PrivateKey, endpoint.Fingerprint = strings.TrimSpace(endpoint.PublicKey), strings.TrimSpace(endpoint.PrivateKey), strings.TrimSpace(endpoint.Fingerprint)
		if endpoint.Fingerprint == "" {
			endpoint.Fingerprint = "chrome"
		}
		if endpoint.InboundID < 1 || endpoint.Port < 1 || endpoint.Port > 65535 || endpoint.RemoteNodeID < 0 || endpoint.Tag == "" || len(endpoint.Tag) > 128 || endpoint.DisplayName == "" || endpoint.TotalBytes < 0 || endpoint.UsedBytes < 0 || endpoint.TrafficResetDay < 0 || endpoint.TrafficResetDay > 31 || net.ParseIP(endpoint.Listen) == nil || invalidHostPort(endpoint.Target) || len(endpoint.ServerNames) == 0 || len(endpoint.ShortIDs) == 0 || !validX25519(endpoint.PrivateKey) || !validX25519(endpoint.PublicKey) || !endpoint.VLESSEnabled && !endpoint.HY2Enabled {
			return errors.New("meridian runtime: invalid legacy endpoint")
		}
		if endpoint.HY2Configured {
			if strings.TrimSpace(endpoint.HY2Tag) == "" || strings.TrimSpace(endpoint.HY2ServerName) == "" || strings.TrimSpace(endpoint.HY2Certificate) == "" || strings.TrimSpace(endpoint.HY2PrivateKey) == "" {
				return errors.New("meridian runtime: incomplete legacy Hysteria endpoint")
			}
		} else if endpoint.HY2Enabled || endpoint.HY2Tag != "" || endpoint.HY2ServerName != "" || endpoint.HY2Certificate != "" || endpoint.HY2PrivateKey != "" {
			return errors.New("meridian runtime: inconsistent legacy Hysteria endpoint")
		}
		for _, name := range endpoint.ServerNames {
			if strings.TrimSpace(name) == "" || len(name) > 253 {
				return errors.New("meridian runtime: invalid legacy endpoint server name")
			}
		}
		for _, shortID := range endpoint.ShortIDs {
			decoded, err := hex.DecodeString(shortID)
			if err != nil || len(decoded) == 0 || len(decoded) > 8 || strings.ToLower(shortID) != shortID {
				return errors.New("meridian runtime: invalid legacy endpoint short id")
			}
		}
		if _, exists := endpoints[endpoint.InboundID]; exists {
			return errors.New("meridian runtime: duplicate legacy endpoint")
		}
		endpoints[endpoint.InboundID] = *endpoint
	}
	seenRoutes := map[string]bool{}
	for _, route := range r.Export.Routes {
		if route.ID == "" || len(route.ID) > 128 || route.ParentIdentityHash == "" || route.InboundID < 1 || route.InboundTag == "" || route.BaseUser == "" || route.FixedUser == "" || uuid.Validate(route.FixedUUID) != nil || route.EgressNodeID == "" || route.Revision == 0 || route.UsageBaseline < 0 || route.UsageObserved < route.UsageBaseline || seenRoutes[route.ID] {
			return errors.New("meridian runtime: invalid legacy route")
		}
		parent, hasParent := clientsByIdentity[route.ParentIdentityHash]
		endpoint, hasEndpoint := endpoints[route.InboundID]
		if !hasParent || !hasEndpoint || route.BaseUser != parent.Email || route.InboundTag != endpoint.Tag {
			return errors.New("meridian runtime: orphaned legacy route")
		}
		seenRoutes[route.ID] = true
	}
	for _, client := range r.Export.Clients {
		for inboundID, auth := range client.HY2AuthByInbound {
			if !slices.Contains(client.InboundIDs, inboundID) || strings.TrimSpace(auth) == "" || len(auth) > 512 {
				return errors.New("meridian runtime: client Hysteria credential inventory contains an unmanaged entry")
			}
		}
		for _, inboundID := range client.InboundIDs {
			endpoint, exists := endpoints[inboundID]
			if !exists {
				return errors.New("meridian runtime: client references an unmanaged legacy endpoint")
			}
			auth, hasAuth := client.HY2AuthByInbound[inboundID]
			if endpoint.HY2Configured && (!hasAuth || strings.TrimSpace(auth) == "" || len(auth) > 512) || !endpoint.HY2Configured && hasAuth {
				return errors.New("meridian runtime: client Hysteria credential inventory is incomplete")
			}
		}
	}
	return nil
}

func LegacyIdentityFingerprint(identity string) string {
	digest := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(identity))))
	return hex.EncodeToString(digest[:])
}

func validLegacyToken(value string) bool {
	if len(value) < 8 || len(value) > 256 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character <= 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validX25519(value string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func invalidHostPort(value string) bool {
	host, port, err := net.SplitHostPort(value)
	return err != nil || strings.TrimSpace(host) == "" || port != "443"
}
