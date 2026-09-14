package client

import (
	"context"
	"net/http"
	"net/url"

	"github.com/dopeCape/better-nm/internal/core"
)

// Secret-agent routes, added ahead of the daemon side so the TUI and desktop
// prompts compile against *Client: the backend branch provides the same four
// methods over the same routes, and this file goes away when it lands.

// PendingSecrets is GET /secrets: every request NetworkManager is waiting on.
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

// AnswerSecret is POST /secrets/{id}: hands the typed secrets to NetworkManager.
func (c *Client) AnswerSecret(ctx context.Context, id string, a core.SecretAnswer) error {
	return c.do(ctx, http.MethodPost, "/secrets/"+url.PathEscape(id), nil, a, nil)
}

// CancelSecret is POST /secrets/{id}/cancel: tells NetworkManager nobody will answer.
func (c *Client) CancelSecret(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/secrets/"+url.PathEscape(id)+"/cancel", nil, nil, nil)
}
