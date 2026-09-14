package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/dopeCape/better-nm/internal/api"
	"github.com/dopeCape/better-nm/internal/config"
	"github.com/dopeCape/better-nm/internal/core"
)

// StreamItem is one item of Events: exactly one of Change or Event is set.
type StreamItem = api.StreamItem

// Status is GET /status.
func (c *Client) Status(ctx context.Context) (api.StatusResponse, error) {
	var out api.StatusResponse
	err := c.do(ctx, http.MethodGet, "/status", nil, nil, &out)
	return out, err
}

// Devices is GET /devices.
func (c *Client) Devices(ctx context.Context) ([]core.Device, error) {
	var out []core.Device
	err := c.do(ctx, http.MethodGet, "/devices", nil, nil, &out)
	return out, err
}

// Wifi is GET /wifi?device=; device "" lists every Wi-Fi device's networks.
func (c *Client) Wifi(ctx context.Context, device string) ([]core.WifiNetwork, error) {
	q := url.Values{}
	if device != "" {
		q.Set("device", device)
	}
	var out []core.WifiNetwork
	err := c.do(ctx, http.MethodGet, "/wifi", q, nil, &out)
	return out, err
}

// ScanWifi is POST /wifi/scan.
func (c *Client) ScanWifi(ctx context.Context, device string) error {
	return c.do(ctx, http.MethodPost, "/wifi/scan", nil, api.DeviceRequest{Device: device}, nil)
}

// ConnectWifi is POST /wifi/connect.
func (c *Client) ConnectWifi(ctx context.Context, req core.ConnectWifiRequest) error {
	return c.do(ctx, http.MethodPost, "/wifi/connect", nil, req, nil)
}

// DisconnectWifi is POST /wifi/disconnect; device "" = the first Wi-Fi device.
func (c *Client) DisconnectWifi(ctx context.Context, device string) error {
	return c.do(ctx, http.MethodPost, "/wifi/disconnect", nil, api.DeviceRequest{Device: device}, nil)
}

// ForgetWifi is POST /wifi/forget.
func (c *Client) ForgetWifi(ctx context.Context, uuid string) error {
	return c.do(ctx, http.MethodPost, "/wifi/forget", nil, api.UUIDRequest{UUID: uuid}, nil)
}

// SetWifiEnabled is POST /wifi/enabled.
func (c *Client) SetWifiEnabled(ctx context.Context, on bool) error {
	return c.do(ctx, http.MethodPost, "/wifi/enabled", nil, api.OnRequest{On: on}, nil)
}

// Profiles is GET /profiles.
func (c *Client) Profiles(ctx context.Context) ([]core.Profile, error) {
	var out []core.Profile
	err := c.do(ctx, http.MethodGet, "/profiles", nil, nil, &out)
	return out, err
}

// Profile is GET /profiles/{uuid}.
func (c *Client) Profile(ctx context.Context, uuid string) (core.Profile, error) {
	var out core.Profile
	err := c.do(ctx, http.MethodGet, "/profiles/"+url.PathEscape(uuid), nil, nil, &out)
	return out, err
}

// SetProfileIP is PUT /profiles/{uuid}/ip; a nil family is left as is.
func (c *Client) SetProfileIP(ctx context.Context, uuid string, ipv4, ipv6 *core.IPConfig) error {
	return c.do(ctx, http.MethodPut, "/profiles/"+url.PathEscape(uuid)+"/ip", nil, api.IPRequest{IPv4: ipv4, IPv6: ipv6}, nil)
}

// ActivateProfile is POST /profiles/{uuid}/activate; device "" lets NM choose.
func (c *Client) ActivateProfile(ctx context.Context, uuid, device string) error {
	return c.do(ctx, http.MethodPost, "/profiles/"+url.PathEscape(uuid)+"/activate", nil, api.DeviceRequest{Device: device}, nil)
}

// DeactivateProfile is POST /profiles/{uuid}/deactivate.
func (c *Client) DeactivateProfile(ctx context.Context, uuid string) error {
	return c.do(ctx, http.MethodPost, "/profiles/"+url.PathEscape(uuid)+"/deactivate", nil, nil, nil)
}

// SetAutoconnect is POST /profiles/{uuid}/autoconnect.
func (c *Client) SetAutoconnect(ctx context.Context, uuid string, on bool) error {
	return c.do(ctx, http.MethodPost, "/profiles/"+url.PathEscape(uuid)+"/autoconnect", nil, api.OnRequest{On: on}, nil)
}

// DeleteProfile is DELETE /profiles/{uuid}.
func (c *Client) DeleteProfile(ctx context.Context, uuid string) error {
	return c.do(ctx, http.MethodDelete, "/profiles/"+url.PathEscape(uuid), nil, nil, nil)
}

// ActiveConnections is GET /active.
func (c *Client) ActiveConnections(ctx context.Context) ([]core.ActiveConnection, error) {
	var out []core.ActiveConnection
	err := c.do(ctx, http.MethodGet, "/active", nil, nil, &out)
	return out, err
}

// VPNs is GET /vpn.
func (c *Client) VPNs(ctx context.Context) ([]core.VPN, error) {
	var out []core.VPN
	err := c.do(ctx, http.MethodGet, "/vpn", nil, nil, &out)
	return out, err
}

// ConnectVPN is POST /vpn/{id}/connect.
func (c *Client) ConnectVPN(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/vpn/"+url.PathEscape(id)+"/connect", nil, nil, nil)
}

// DisconnectVPN is POST /vpn/{id}/disconnect.
func (c *Client) DisconnectVPN(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/vpn/"+url.PathEscape(id)+"/disconnect", nil, nil, nil)
}

// ImportVPN is POST /vpn/import.
func (c *Client) ImportVPN(ctx context.Context, req api.ImportVPNRequest) (api.ImportVPNResult, error) {
	var out api.ImportVPNResult
	err := c.do(ctx, http.MethodPost, "/vpn/import", nil, req, &out)
	return out, err
}

// SetTailscaleExitNode is POST /vpn/tailscale/exit-node; peer "" clears it.
func (c *Client) SetTailscaleExitNode(ctx context.Context, peer string, allowLAN bool) error {
	return c.do(ctx, http.MethodPost, "/vpn/tailscale/exit-node", nil, api.ExitNodeRequest{Peer: peer, AllowLAN: allowLAN}, nil)
}

// SetTailscaleExitNodeEnabled is POST /vpn/tailscale/exit-node/enabled.
func (c *Client) SetTailscaleExitNodeEnabled(ctx context.Context, on bool) error {
	return c.do(ctx, http.MethodPost, "/vpn/tailscale/exit-node/enabled", nil, api.OnRequest{On: on}, nil)
}

// TailscaleLogin is POST /vpn/tailscale/login; returns the URL to open.
func (c *Client) TailscaleLogin(ctx context.Context) (string, error) {
	var out api.LoginResponse
	err := c.do(ctx, http.MethodPost, "/vpn/tailscale/login", nil, nil, &out)
	return out.URL, err
}

// TailscaleLogout is POST /vpn/tailscale/logout.
func (c *Client) TailscaleLogout(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/vpn/tailscale/logout", nil, nil, nil)
}

// SetTailscaleAcceptDNS is POST /vpn/tailscale/accept-dns.
func (c *Client) SetTailscaleAcceptDNS(ctx context.Context, on bool) error {
	return c.do(ctx, http.MethodPost, "/vpn/tailscale/accept-dns", nil, api.OnRequest{On: on}, nil)
}

// Monitor is GET /monitor.
func (c *Client) Monitor(ctx context.Context) (core.MonitorStatus, error) {
	var out core.MonitorStatus
	err := c.do(ctx, http.MethodGet, "/monitor", nil, nil, &out)
	return out, err
}

// Samples is GET /monitor/samples; key "" = the current network, anchor "" = all, limit 0 = server default.
func (c *Client) Samples(ctx context.Context, key, anchor string, limit int) ([]core.Sample, error) {
	q := url.Values{}
	if key != "" {
		q.Set("key", key)
	}
	if anchor != "" {
		q.Set("anchor", anchor)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out []core.Sample
	err := c.do(ctx, http.MethodGet, "/monitor/samples", q, nil, &out)
	return out, err
}

// ResetBaseline is POST /monitor/baseline/reset; key "" = the current network.
func (c *Client) ResetBaseline(ctx context.Context, key string) error {
	return c.do(ctx, http.MethodPost, "/monitor/baseline/reset", nil, api.KeyRequest{Key: key}, nil)
}

// PauseMonitor is POST /monitor/pause.
func (c *Client) PauseMonitor(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/monitor/pause", nil, nil, nil)
}

// ResumeMonitor is POST /monitor/resume.
func (c *Client) ResumeMonitor(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/monitor/resume", nil, nil, nil)
}

// Speed is POST /speed: it streams progress (progress may be nil) and returns the result.
func (c *Client) Speed(ctx context.Context, opts core.SpeedOptions, progress func(core.SpeedProgress)) (core.SpeedResult, error) {
	if progress == nil {
		var out core.SpeedResult
		err := c.do(ctx, http.MethodPost, "/speed", url.Values{"wait": {"1"}}, opts, &out)
		return out, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/speed", nil, opts)
	if err != nil {
		return core.SpeedResult{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return core.SpeedResult{}, c.wrapTransport(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return core.SpeedResult{}, decodeError(resp)
	}
	var result core.SpeedResult
	var got bool
	var apiErr error
	err = readSSE(resp.Body, func(ev sseEvent) bool {
		switch ev.Name {
		case api.SSEProgress:
			var p core.SpeedProgress
			if json.Unmarshal(ev.Data, &p) == nil {
				progress(p)
			}
		case api.SSEResult:
			if json.Unmarshal(ev.Data, &result) == nil {
				got = true
			}
			return false
		case api.SSEError:
			var er api.ErrorResponse
			if json.Unmarshal(ev.Data, &er) == nil {
				apiErr = &APIError{Status: api.StatusFor(core.Errorf(core.ErrorKind(er.Code), "", "")), Code: core.ErrorKind(er.Code), Message: er.Error, Hint: er.Hint}
			} else {
				apiErr = errors.New("client: speed test failed")
			}
			return false
		}
		return true
	})
	if apiErr != nil {
		return core.SpeedResult{}, apiErr
	}
	if err != nil {
		if ctx.Err() != nil {
			return core.SpeedResult{}, ctx.Err()
		}
		return core.SpeedResult{}, fmt.Errorf("client: speed stream: %w", err)
	}
	if !got {
		if ctx.Err() != nil {
			return core.SpeedResult{}, ctx.Err()
		}
		return core.SpeedResult{}, errors.New("client: speed stream ended without a result")
	}
	return result, nil
}

// SpeedHistory is GET /speed/history; key "" = every network, limit 0 = server default.
func (c *Client) SpeedHistory(ctx context.Context, key string, limit int) ([]core.SpeedResult, error) {
	q := url.Values{}
	if key != "" {
		q.Set("key", key)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out []core.SpeedResult
	err := c.do(ctx, http.MethodGet, "/speed/history", q, nil, &out)
	return out, err
}

// EventHistory is GET /events?limit=: stored events, oldest first.
func (c *Client) EventHistory(ctx context.Context, limit int) ([]core.Event, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out []core.Event
	err := c.do(ctx, http.MethodGet, "/events", q, nil, &out)
	return out, err
}

// Events is GET /events/stream: live changes and events until ctx ends or the
// daemon goes away; the channel is closed then.
func (c *Client) Events(ctx context.Context) (<-chan StreamItem, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/events/stream", nil, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.wrapTransport(err)
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, decodeError(resp)
	}
	ch := make(chan StreamItem, 64)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		_ = readSSE(resp.Body, func(ev sseEvent) bool {
			var item StreamItem
			switch ev.Name {
			case api.SSEChange:
				var chg core.Change
				if json.Unmarshal(ev.Data, &chg) != nil {
					return true
				}
				item.Change = &chg
			case api.SSEEvent:
				var e core.Event
				if json.Unmarshal(ev.Data, &e) != nil {
					return true
				}
				item.Event = &e
			default:
				return true
			}
			select {
			case ch <- item:
				return true
			case <-ctx.Done():
				return false
			}
		})
	}()
	return ch, nil
}

// LANHosts is GET /diag/lan.
func (c *Client) LANHosts(ctx context.Context, device string, sweep bool) ([]core.LANHost, error) {
	q := url.Values{}
	if device != "" {
		q.Set("device", device)
	}
	if sweep {
		q.Set("sweep", "1")
	}
	var out []core.LANHost
	err := c.do(ctx, http.MethodGet, "/diag/lan", q, nil, &out)
	return out, err
}

// ListeningPorts is GET /diag/ports.
func (c *Client) ListeningPorts(ctx context.Context) ([]core.ListeningPort, error) {
	var out []core.ListeningPort
	err := c.do(ctx, http.MethodGet, "/diag/ports", nil, nil, &out)
	return out, err
}

// Routes is GET /diag/routes.
func (c *Client) Routes(ctx context.Context) ([]core.Route, error) {
	var out []core.Route
	err := c.do(ctx, http.MethodGet, "/diag/routes", nil, nil, &out)
	return out, err
}

// DNSLookup is GET /diag/dns; server "" = system resolver, qtype "" = A.
func (c *Client) DNSLookup(ctx context.Context, name, server, qtype string) (core.DNSAnswer, error) {
	q := url.Values{"name": {name}}
	if server != "" {
		q.Set("server", server)
	}
	if qtype != "" {
		q.Set("type", qtype)
	}
	var out core.DNSAnswer
	err := c.do(ctx, http.MethodGet, "/diag/dns", q, nil, &out)
	return out, err
}

// PublicIP is GET /diag/public-ip.
func (c *Client) PublicIP(ctx context.Context) (core.PublicIP, error) {
	var out core.PublicIP
	err := c.do(ctx, http.MethodGet, "/diag/public-ip", nil, nil, &out)
	return out, err
}

// Infra is GET /diag/infra.
func (c *Client) Infra(ctx context.Context) ([]core.InfraNetwork, error) {
	var out []core.InfraNetwork
	err := c.do(ctx, http.MethodGet, "/diag/infra", nil, nil, &out)
	return out, err
}

// PendingSecrets is GET /secrets: NetworkManager's open password prompts.
func (c *Client) PendingSecrets(ctx context.Context) ([]core.SecretRequest, error) {
	var out []core.SecretRequest
	err := c.do(ctx, http.MethodGet, "/secrets", nil, nil, &out)
	return out, err
}

// Secret is GET /secrets/{id}.
func (c *Client) Secret(ctx context.Context, id string) (core.SecretRequest, error) {
	var out core.SecretRequest
	err := c.do(ctx, http.MethodGet, "/secrets/"+url.PathEscape(id), nil, nil, &out)
	return out, err
}

// AnswerSecret is POST /secrets/{id}: 404 when the request is gone, 409 when
// it was already answered or cancelled.
func (c *Client) AnswerSecret(ctx context.Context, id string, a core.SecretAnswer) error {
	return c.do(ctx, http.MethodPost, "/secrets/"+url.PathEscape(id), nil, a, nil)
}

// CancelSecret is POST /secrets/{id}/cancel.
func (c *Client) CancelSecret(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/secrets/"+url.PathEscape(id)+"/cancel", nil, nil, nil)
}

// Config is GET /config.
func (c *Client) Config(ctx context.Context) (config.Config, error) {
	var out config.Config
	err := c.do(ctx, http.MethodGet, "/config", nil, nil, &out)
	return out, err
}

// SetConfig is PUT /config; returns the whole new configuration.
func (c *Client) SetConfig(ctx context.Context, key, value string) (config.Config, error) {
	var out config.Config
	err := c.do(ctx, http.MethodPut, "/config", nil, api.ConfigSetRequest{Key: key, Value: value}, &out)
	return out, err
}

// NotifyTest is POST /notify/test.
func (c *Client) NotifyTest(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/notify/test", nil, nil, nil)
}

// Raw performs an arbitrary request under /v1 and returns the body; for
// debugging and `bnm api` style escape hatches.
func (c *Client) Raw(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	req, err := c.newRequest(ctx, method, path, nil, body)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, c.wrapTransport(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return data, resp.StatusCode, err
}
