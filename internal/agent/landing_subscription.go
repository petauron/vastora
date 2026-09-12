package agent

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

const landingSubscriptionMaxBytes = 4 << 20

// This is an in-process Agent endpoint, bound only to the installed private
// service address. It cannot forward management paths or arbitrary upstreams.
func (s *Store) landingSubscriptionHandler(client *http.Client) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, private")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		token, clash, ok := landingSubscriptionRequest(r)
		if !ok {
			http.NotFound(w, r)
			return
		}
		installation, err := s.AppliedInstallation(r.Context(), threeXUIKey)
		if err != nil {
			http.Error(w, "暂时无法获取订阅", http.StatusServiceUnavailable)
			return
		}
		state, err := s.landingController(r.Context())
		if err != nil {
			http.Error(w, "暂时无法获取订阅", http.StatusServiceUnavailable)
			return
		}
		var account *landingControllerAccount
		grants := []landing.SubscriptionGrant{}
		if state != nil {
			for _, grant := range state.Grants {
				if sameSubscriptionToken(token, grant.ChildSubscription) {
					http.NotFound(w, r)
					return
				}
			}
			for _, value := range state.Accounts {
				if sameSubscriptionToken(token, value.SubscriptionToken) {
					if account != nil {
						http.Error(w, "暂时无法获取订阅", http.StatusServiceUnavailable)
						return
					}
					copy := value
					account = &copy
				}
			}
			if account != nil {
				_, used, quotaErr := landing.AllocateQuota(account.Total, account.Enabled, account.Members)
				if quotaErr != nil || account.Blocked || account.Deleted || account.PendingOperation != "" || !account.Enabled || !account.Mode.Valid() || account.Expiry > 0 && account.Expiry <= s.now().UnixMilli() || account.Total > 0 && used >= account.Total {
					http.NotFound(w, r)
					return
				}
				for _, grant := range state.Grants {
					if grant.Task.Grant.ParentID != account.ID || grant.Phase != "ready" || !grant.Task.Grant.Enabled {
						continue
					}
					grants = append(grants, landing.SubscriptionGrant{Grant: grant.Task.Grant.Published(account.Mode), EntryName: grant.Task.EntryName, LandingName: grant.Task.LandingName, BaseLink: grant.Material.BaseLink, FixedLink: grant.Material.FixedLink})
				}
				slices.SortFunc(grants, func(a, b landing.SubscriptionGrant) int { return strings.Compare(a.Grant.ID, b.Grant.ID) })
			}
		}
		path := threeXUIRawSubscriptionPath
		if clash {
			path = threeXUIClashSubscriptionPath
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		upstream, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(installation.ServiceAddress, strconv.Itoa(threeXUISubscriptionPort))+path+token, nil)
		if err != nil {
			http.Error(w, "暂时无法获取订阅", http.StatusServiceUnavailable)
			return
		}
		upstream.Host = r.Host
		// Select a known native representation instead of forwarding arbitrary
		// converter/query parameters or authorization headers.
		upstream.Header.Set("User-Agent", "Vastora-Subscription")
		if clash {
			upstream.Header.Set("User-Agent", "Mihomo")
		}
		response, err := client.Do(upstream)
		if err != nil {
			http.Error(w, "暂时无法获取订阅", http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, landingSubscriptionMaxBytes+1))
		if err != nil || len(body) > landingSubscriptionMaxBytes {
			http.Error(w, "暂时无法获取订阅", http.StatusBadGateway)
			return
		}
		if account != nil && len(grants) > 0 {
			if clash {
				body, err = landing.ComposeMihomo(body, account.ID, account.Mode, grants)
			} else {
				_, decodeErr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(body)))
				body, err = landing.ComposeLinks(body, account.ID, account.Mode, grants, decodeErr == nil)
			}
			if err != nil {
				http.Error(w, "暂时无法生成此格式的订阅", http.StatusUnprocessableEntity)
				return
			}
		}
		for _, key := range []string{"Content-Type", "Content-Disposition", "Profile-Update-Interval", "Profile-Title", "Subscription-Userinfo", "Profile-Web-Page-Url"} {
			if value := response.Header.Get(key); value != "" {
				w.Header().Set(key, value)
			}
		}
		if account != nil {
			_, used, _ := landing.AllocateQuota(account.Total, account.Enabled, account.Members)
			w.Header().Set("Subscription-Userinfo", fmt.Sprintf("upload=0; download=%d; total=%d; expire=%d", used, account.Total, account.Expiry/1000))
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(body)
		}
	})
}

func sameSubscriptionToken(a, b string) bool {
	return b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func landingSubscriptionRequest(r *http.Request) (string, bool, bool) {
	if r.URL.RawPath != "" || r.URL.RawQuery != "" {
		return "", false, false
	}
	path := r.URL.Path
	clash := strings.HasPrefix(path, threeXUIClashSubscriptionPath)
	prefix := threeXUIRawSubscriptionPath
	if clash {
		prefix = threeXUIClashSubscriptionPath
	}
	if !strings.HasPrefix(path, prefix) {
		return "", false, false
	}
	token := strings.TrimPrefix(path, prefix)
	if len(token) < 8 || len(token) > 128 {
		return "", false, false
	}
	for _, c := range token {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return "", false, false
		}
	}
	ua := strings.ToLower(r.UserAgent())
	return token, clash || strings.Contains(ua, "clash") || strings.Contains(ua, "mihomo"), true
}

func (s *Store) runLandingSubscriptions(ctx context.Context, report func(error)) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, MaxIdleConns: 16, IdleConnTimeout: 30 * time.Second}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var server *http.Server
	var address string
	stop := func() {
		s.landingSubscriptionMu.Lock()
		s.landingSubscriptionAddress = ""
		s.landingSubscriptionMu.Unlock()
		if server != nil {
			_ = server.Close()
			server = nil
		}
		address = ""
	}
	defer stop()
	for {
		installation, err := s.AppliedInstallation(ctx, threeXUIKey)
		if err != nil || (landing.ServerPlan{Revision: 1, Address: installation.ServiceAddress}).Validate() != nil {
			stop()
		} else if address != installation.ServiceAddress {
			stop()
			listener, err := net.Listen("tcp4", net.JoinHostPort(installation.ServiceAddress, strconv.Itoa(landing.SubscriptionPort)))
			if err != nil {
				if report != nil {
					report(errors.New("agent: private subscription endpoint is unavailable"))
				}
			} else {
				address = installation.ServiceAddress
				server = &http.Server{Handler: s.landingSubscriptionHandler(client), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
				s.landingSubscriptionMu.Lock()
				s.landingSubscriptionAddress = address
				s.landingSubscriptionMu.Unlock()
				go func(serving *http.Server, bound string) {
					if err := serving.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
						s.landingSubscriptionMu.Lock()
						if s.landingSubscriptionAddress == bound {
							s.landingSubscriptionAddress = ""
						}
						s.landingSubscriptionMu.Unlock()
						if report != nil {
							report(errors.New("agent: private subscription endpoint stopped"))
						}
					}
				}(server, address)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		s.landingSubscriptionMu.RLock()
		healthy := s.landingSubscriptionAddress == address
		s.landingSubscriptionMu.RUnlock()
		if !healthy {
			stop()
		}
	}
}
