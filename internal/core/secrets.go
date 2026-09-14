package core

import (
	"context"
	"time"
)

// SecretField is one value NetworkManager is asking for.
type SecretField struct {
	// Key is the NM secret key inside the setting: "psk" for Wi-Fi, "password",
	// "cert-pass", "http-proxy-password", "challenge-response" (OTP) for VPNs, ...
	Key string `json:"key"`
	// Label is the human prompt ("Wi-Fi password", "One-time code").
	Label string `json:"label"`
	// Secret says the input must be masked. Usernames are not secret.
	Secret bool `json:"secret"`
}

// SecretRequest is an outstanding GetSecrets call from NetworkManager, waiting for
// a person to answer through one of the surfaces.
type SecretRequest struct {
	ID             string        `json:"id"`
	ConnectionUUID string        `json:"connection_uuid"`
	ConnectionName string        `json:"connection_name"`
	SSID           string        `json:"ssid,omitempty"`
	VPN            bool          `json:"vpn"`
	VPNKind        string        `json:"vpn_kind,omitempty"` // OpenVPN, WireGuard, ...
	SettingName    string        `json:"setting_name"`       // 802-11-wireless-security, vpn, ...
	Fields         []SecretField `json:"fields"`
	// Message is free text from the VPN plugin (NM "x-vpn-message" hint), if any.
	Message string `json:"message,omitempty"`
	// RequestNew is set when NM already tried the stored secret and it failed
	// (wrong password): the prompt should say so.
	RequestNew bool `json:"request_new"`
	// UserRequested is set when a user action (not autoconnect) triggered this.
	UserRequested bool      `json:"user_requested"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// SecretAnswer is what a surface sends back for a SecretRequest.
type SecretAnswer struct {
	// Secrets maps SecretField.Key to the value typed by the user.
	Secrets map[string]string `json:"secrets"`
	// Save asks NM to persist the secrets in the profile (system-owned, flags 0)
	// so the next activation needs no prompt.
	Save bool `json:"save"`
}

// SecretOutcome is how a request ended.
type SecretOutcome string

const (
	SecretAnswered  SecretOutcome = "answered"
	SecretCancelled SecretOutcome = "cancelled"
	SecretTimeout   SecretOutcome = "timeout"
)

// SecretBroker is the daemon-side surface the API drives; the NM secret agent
// implementation fulfils it.
type SecretBroker interface {
	Pending(ctx context.Context) ([]SecretRequest, error)
	Answer(ctx context.Context, id string, a SecretAnswer) error
	Cancel(ctx context.Context, id string) error
}
