package agent

import (
	"encoding/json"
	"errors"
	"github.com/petauron/glivela"
	"strings"
)

// observeMeridianState decodes a retained artifact without contacting or managing a runtime.
func observeMeridianState(artifact glivela.DesiredArtifact) ([]ApplicationEndpointObservation, error) {
	if artifact.Validate() != nil {
		return nil, errors.New("agent: Glivela runtime artifact is invalid")
	}
	var config struct {
		Inbounds []json.RawMessage `json:"inbounds"`
	}
	if json.Unmarshal(artifact.Config, &config) != nil {
		return nil, errors.New("agent: Glivela Xray configuration is invalid")
	}
	var realityTag, hysteriaTag, listen string
	var port int
	for _, raw := range config.Inbounds {
		var inbound struct {
			Protocol string `json:"protocol"`
			Port     int    `json:"port"`
			Listen   string `json:"listen"`
			Tag      string `json:"tag"`
			Total    int64  `json:"total"`
			Stream   struct {
				Method   string `json:"method"`
				Security string `json:"security"`
			} `json:"streamSettings"`
		}
		if json.Unmarshal(raw, &inbound) != nil {
			return nil, errors.New("agent: Glivela runtime contains an invalid endpoint")
		}
		if inbound.Tag == "api" {
			continue
		}
		if inbound.Port < 1 || inbound.Port > 65535 || strings.TrimSpace(inbound.Tag) == "" {
			return nil, errors.New("agent: Glivela runtime contains an invalid endpoint")
		}
		method := strings.ToLower(strings.TrimSpace(inbound.Stream.Method))
		security := strings.ToLower(strings.TrimSpace(inbound.Stream.Security))
		switch {
		case inbound.Protocol == "vless" && method == "raw" && security == "reality":
			if realityTag != "" {
				return nil, errors.New("agent: Glivela runtime contains multiple REALITY endpoints")
			}
			realityTag, listen, port = strings.TrimSpace(inbound.Tag), strings.TrimSpace(inbound.Listen), inbound.Port
		case inbound.Protocol == "hysteria" && method == "hysteria" && security == "tls":
			if hysteriaTag != "" {
				return nil, errors.New("agent: Glivela runtime contains multiple Hysteria endpoints")
			}
			hysteriaTag = strings.TrimSpace(inbound.Tag)
			if realityTag == "" {
				listen, port = strings.TrimSpace(inbound.Listen), inbound.Port
			}
		default:
			return nil, errors.New("agent: Glivela runtime contains an unsupported endpoint")
		}
	}
	selectedTag := realityTag
	if selectedTag == "" {
		selectedTag = hysteriaTag
	}
	if selectedTag == "" || port < 1 {
		return nil, errors.New("agent: Glivela runtime has no managed endpoint")
	}
	return []ApplicationEndpointObservation{{
		AppKey: meridianKey, Name: "inbound-1", Protocol: "tcp",
		AppProtocol: "meridian/entry", Listen: listen, Port: port,
		Enabled: true, InboundTag: selectedTag,
	}}, nil
}
