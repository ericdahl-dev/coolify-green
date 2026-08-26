// Package coolify is a client for the Coolify v1 REST API.
package coolify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrUnauthorized means the API token was rejected.
	ErrUnauthorized = errors.New("unauthorized: check the Coolify API token")
	// ErrRateLimit means the instance returned HTTP 429.
	ErrRateLimit = errors.New("rate limited by Coolify")
	// ErrNotFound means the resource does not exist on the instance.
	ErrNotFound = errors.New("not found")
)

// RateLimitError carries the Retry-After hint from a 429 response.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("%v (retry after %v)", ErrRateLimit, e.RetryAfter)
}

func (e *RateLimitError) Unwrap() error { return ErrRateLimit }

// Client talks to one Coolify instance.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New creates a Client for the given instance. baseURL may be given with or
// without a scheme and with or without a trailing slash.
func New(baseURL, token string) (*Client, error) {
	base, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("coolify: API token is empty")
	}
	return &Client{
		baseURL: base,
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// NormalizeBaseURL trims trailing slashes and defaults a missing scheme to
// https, so a config holding "coolify.example.com" still works.
func NormalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("coolify: instance url is empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("coolify: invalid instance url %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("coolify: instance url must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("coolify: instance url %q has no host", raw)
	}
	return strings.TrimRight(u.Scheme+"://"+u.Host+u.Path, "/"), nil
}

// BaseURL returns the normalized instance URL.
func (c *Client) BaseURL() string { return c.baseURL }

func (c *Client) do(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+"/api/v1"+path, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrUnauthorized
	case http.StatusNotFound:
		return fmt.Errorf("%s: %w", path, ErrNotFound)
	case http.StatusTooManyRequests:
		return &RateLimitError{RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s: decoding response: %w", path, err)
	}
	return nil
}

func parseRetryAfter(s string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 60 * time.Second
	}
	return time.Duration(n) * time.Second
}

// Projects lists every project on the instance. The list endpoint omits
// environments, so Project must be called per project to map resources to
// their project (see Topology).
func (c *Client) Projects(ctx context.Context) ([]Project, error) {
	var out []Project
	return out, c.do(ctx, http.MethodGet, "/projects", &out)
}

// Project fetches one project including its environments.
func (c *Client) Project(ctx context.Context, uuid string) (Project, error) {
	var out Project
	return out, c.do(ctx, http.MethodGet, "/projects/"+url.PathEscape(uuid), &out)
}

// Servers lists the servers registered with the instance.
func (c *Client) Servers(ctx context.Context) ([]Server, error) {
	var out []Server
	return out, c.do(ctx, http.MethodGet, "/servers", &out)
}

func (c *Client) resources(ctx context.Context, path string, kind Kind) ([]Resource, error) {
	var out []Resource
	if err := c.do(ctx, http.MethodGet, path, &out); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Kind = kind
	}
	return out, nil
}

// Applications lists every application on the instance.
func (c *Client) Applications(ctx context.Context) ([]Resource, error) {
	return c.resources(ctx, "/applications", KindApplication)
}

// Services lists every service on the instance.
func (c *Client) Services(ctx context.Context) ([]Resource, error) {
	return c.resources(ctx, "/services", KindService)
}

// Databases lists every database on the instance.
func (c *Client) Databases(ctx context.Context) ([]Resource, error) {
	return c.resources(ctx, "/databases", KindDatabase)
}

// ActiveDeployments lists deployments that are currently queued or running,
// across every application. This is one call for the whole instance, which is
// what makes per-cycle polling cheap.
func (c *Client) ActiveDeployments(ctx context.Context) ([]Deployment, error) {
	var out []Deployment
	return out, c.do(ctx, http.MethodGet, "/deployments", &out)
}

// LatestDeployment returns the most recent deployment for an application, or
// nil when the application has never been deployed.
func (c *Client) LatestDeployment(ctx context.Context, appUUID string) (*Deployment, error) {
	var page deploymentPage
	path := "/deployments/applications/" + url.PathEscape(appUUID) + "?skip=0&take=1"
	if err := c.do(ctx, http.MethodGet, path, &page); err != nil {
		return nil, err
	}
	if len(page.Deployments) == 0 {
		return nil, nil
	}
	d := page.Deployments[0]
	return &d, nil
}

// Deployment fetches a single deployment by uuid, including its logs.
func (c *Client) Deployment(ctx context.Context, deploymentUUID string) (Deployment, error) {
	var out Deployment
	return out, c.do(ctx, http.MethodGet, "/deployments/"+url.PathEscape(deploymentUUID), &out)
}

// Deploy triggers a deployment of a resource. force skips the build cache.
func (c *Client) Deploy(ctx context.Context, uuid string, force bool) error {
	q := url.Values{}
	q.Set("uuid", uuid)
	if force {
		q.Set("force", "true")
	}
	return c.do(ctx, http.MethodPost, "/deploy?"+q.Encode(), nil)
}

// CancelDeployment stops an in-flight deployment.
func (c *Client) CancelDeployment(ctx context.Context, deploymentUUID string) error {
	return c.do(ctx, http.MethodPost, "/deployments/"+url.PathEscape(deploymentUUID)+"/cancel", nil)
}

// kindPath maps a resource kind to its API path segment.
func kindPath(kind Kind) (string, error) {
	switch kind {
	case KindApplication:
		return "applications", nil
	case KindService:
		return "services", nil
	case KindDatabase:
		return "databases", nil
	default:
		return "", fmt.Errorf("coolify: unknown resource kind %q", kind)
	}
}

// Start starts a stopped resource.
func (c *Client) Start(ctx context.Context, kind Kind, uuid string) error {
	return c.lifecycle(ctx, kind, uuid, "start")
}

// Restart restarts a running resource.
func (c *Client) Restart(ctx context.Context, kind Kind, uuid string) error {
	return c.lifecycle(ctx, kind, uuid, "restart")
}

// Stop stops a running resource.
func (c *Client) Stop(ctx context.Context, kind Kind, uuid string) error {
	return c.lifecycle(ctx, kind, uuid, "stop")
}

func (c *Client) lifecycle(ctx context.Context, kind Kind, uuid, action string) error {
	seg, err := kindPath(kind)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/"+seg+"/"+url.PathEscape(uuid)+"/"+action, nil)
}

// ResourceURL builds the Coolify web UI link for a resource. Applications live
// under their project and environment; services and databases use the same
// shape with a different segment.
func ResourceURL(baseURL string, kind Kind, projectUUID, environmentUUID, resourceUUID string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	seg, err := kindPath(kind)
	if baseURL == "" || projectUUID == "" || environmentUUID == "" || err != nil {
		return baseURL + "/dashboard"
	}
	return fmt.Sprintf("%s/project/%s/environment/%s/%s/%s",
		baseURL, projectUUID, environmentUUID, strings.TrimSuffix(seg, "s"), resourceUUID)
}

// ResourceURL builds the web UI link for a resource on this instance.
func (c *Client) ResourceURL(kind Kind, projectUUID, environmentUUID, resourceUUID string) string {
	return ResourceURL(c.baseURL, kind, projectUUID, environmentUUID, resourceUUID)
}
