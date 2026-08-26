// Package webhooks POSTs stuck-resource events to user-configured endpoints.
package webhooks

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ericdahl-dev/coolify-green/internal/config"
	"github.com/ericdahl-dev/coolify-green/internal/logx"
)

// SignatureHeader carries the HMAC-SHA256 signature of the request body when
// the webhook is configured with a secret.
const SignatureHeader = "X-Coolify-Green-Signature"

// Event names.
const (
	EventDeploymentStuck = "deployment_stuck"
	EventResourceStuck   = "resource_stuck"
)

// Reasons carried in Event.Reason.
const (
	ReasonDeployFailed     = "deploy_failed"
	ReasonDeployInProgress = "deploy_in_progress"
	ReasonResourceDown     = "resource_down"
	ReasonResourceUnhealth = "resource_unhealthy"
)

// Event is the JSON payload POSTed to each webhook.
type Event struct {
	Event        string    `json:"event"`
	Reason       string    `json:"reason"`
	Instance     string    `json:"instance"`
	Project      string    `json:"project"`
	ResourceType string    `json:"resource_type"`
	Resource     string    `json:"resource"`
	ResourceUUID string    `json:"resource_uuid"`
	Server       string    `json:"server,omitempty"`
	Status       string    `json:"status,omitempty"`
	Detail       string    `json:"detail,omitempty"`
	URL          string    `json:"url,omitempty"`
	StuckSince   time.Time `json:"stuck_since"`
	Timestamp    time.Time `json:"timestamp"`
}

// Dispatcher sends webhook events to configured endpoints.
type Dispatcher struct {
	hooks  []config.Webhook
	client *http.Client
}

// New creates a Dispatcher for the given webhook configs.
func New(hooks []config.Webhook) *Dispatcher {
	return &Dispatcher{
		hooks:  hooks,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Dispatch POSTs evt to all configured webhooks. Failures are logged but never
// returned — a dead endpoint must not interrupt the poll cycle.
func (d *Dispatcher) Dispatch(evt Event) {
	if len(d.hooks) == 0 {
		return
	}
	body, err := json.Marshal(evt)
	if err != nil {
		logx.Debug("webhook marshal failed", "err", err)
		return
	}
	for _, wh := range d.hooks {
		if err := d.post(wh, body); err != nil {
			logx.Debug("webhook POST failed", "url", wh.URL, "err", err)
		}
	}
}

func (d *Dispatcher) post(wh config.Webhook, body []byte) error {
	req, err := http.NewRequest(http.MethodPost, wh.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if wh.Secret != "" {
		mac := hmac.New(sha256.New, []byte(wh.Secret))
		mac.Write(body)
		req.Header.Set(SignatureHeader, "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("server returned %d", resp.StatusCode)
	}
	return nil
}
