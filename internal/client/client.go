// Package client is a small client for the Monitorable REST v1 API
// (https://api.monitorable.io). It exposes exactly the calls the Terraform
// provider makes, retries transient failures, and turns problem+json
// responses into *APIError. The bearer token is only ever set as a request
// header; it is never logged and is redacted from error text.
package client

import (
	"bytes"
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

const (
	defaultUserAgent = "terraform-provider-monitorable/dev (+https://monitorable.io/docs/api/terraform/)"
	attemptTimeout   = 30 * time.Second
	listPageSize     = 100
	maxBodyBytes     = 8 << 20

	maxIdempotencyKeyLen = 64 // the API's limit on Idempotency-Key
)

// Client talks to one Monitorable API endpoint with one API key.
type Client struct {
	endpoint  string
	apiKey    string
	userAgent string
	http      *http.Client
	sleep     func(context.Context, time.Duration) error // replaced in tests
}

// String never prints the API key, whichever verb formats the client.
func (c Client) String() string {
	return fmt.Sprintf("client.Client{endpoint: %s, apiKey: <redacted>}", c.endpoint)
}

// GoString is String for %#v.
func (c Client) GoString() string { return c.String() }

// New returns a client for endpoint (scheme and host; a trailing slash is
// dropped; a path, query or fragment is refused). apiKey must be non-empty. userAgent defaults to the development
// string when empty.
func New(endpoint, apiKey, userAgent string) (*Client, error) {
	if apiKey == "" {
		return nil, errors.New("the Monitorable API key must not be empty")
	}
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	u, err := url.Parse(endpoint)
	if err == nil && u.User != nil {
		// Never echo the value: userinfo can hold credentials.
		return nil, errors.New("endpoint must be scheme://host[:port] without a path, got a URL with credentials")
	}
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" ||
		u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(endpoint, "?#") {
		return nil, fmt.Errorf("endpoint must be scheme://host[:port] without a path, got %q", endpoint)
	}
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	return &Client{
		endpoint:  endpoint,
		apiKey:    apiKey,
		userAgent: userAgent,
		http: &http.Client{
			Timeout: attemptTimeout,
			// The API never redirects; refusing to follow one keeps the bearer on one host.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		sleep: sleepContext,
	}, nil
}

// call performs one API operation: it JSON-encodes in (if not nil), sends the
// request with retries, maps a non-2xx answer to *APIError, and decodes a
// 2xx body into out (if not nil). ok404 turns the API's own 404 not_found into
// a nil error; a 404 that is not a problem document stays an error.
func (c *Client) call(ctx context.Context, method, path string, query url.Values, hdr map[string]string, in, out any, ok404 bool) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	target := c.endpoint + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return fmt.Errorf("building %s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}

	resp, err := c.do(ctx, req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%s %s: %w", method, path, c.redact(err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return fmt.Errorf("%s %s: reading the response: %w", method, path, c.redact(err))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		ae := newAPIError(resp.StatusCode, resp.Header, raw, c.apiKey)
		if ok404 && ae.IsNotFound() {
			return nil
		}
		return ae
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: decoding the response: %w", method, path, err)
	}
	return nil
}

// redact returns err with the bearer removed from its text.
func (c *Client) redact(err error) error {
	if strings.Contains(err.Error(), c.apiKey) {
		return errors.New(strings.ReplaceAll(err.Error(), c.apiKey, "[redacted]"))
	}
	return err
}

func serverPath(id int64, suffix string) string {
	return "/v1/servers/" + strconv.FormatInt(id, 10) + suffix
}

// Me returns the API key making the request.
func (c *Client) Me(ctx context.Context) (*Me, error) {
	var out Me
	if err := c.call(ctx, http.MethodGet, "/v1/me", nil, nil, nil, &out, false); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateServer creates a server. idempotencyKey is required (1 to 64
// characters): without one the API cannot recognise a retried request, so the
// call is refused before anything is sent. Every retry of the call sends the same
// idempotencyKey, so a lost response cannot create a second server; a retry
// that finds the server already created answers with AgentAPIKey == "" and
// Recovery set.
func (c *Client) CreateServer(ctx context.Context, name, idempotencyKey string) (*CreateServerResponse, error) {
	if idempotencyKey == "" || len(idempotencyKey) > maxIdempotencyKeyLen {
		return nil, fmt.Errorf("idempotency key must be 1 to %d characters, got %d", maxIdempotencyKeyLen, len(idempotencyKey))
	}
	var out CreateServerResponse
	hdr := map[string]string{"Idempotency-Key": idempotencyKey}
	if err := c.call(ctx, http.MethodPost, "/v1/servers", nil, hdr, createServerRequest{Name: name}, &out, false); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetServer returns one server; a missing server is an error for which
// IsNotFound is true.
func (c *Client) GetServer(ctx context.Context, id int64) (*Server, error) {
	var out Server
	if err := c.call(ctx, http.MethodGet, serverPath(id, ""), nil, nil, nil, &out, false); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateServer renames a server.
func (c *Client) UpdateServer(ctx context.Context, id int64, name string) (*Server, error) {
	var out Server
	if err := c.call(ctx, http.MethodPatch, serverPath(id, ""), nil, nil, updateServerRequest{Name: name}, &out, false); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteServer deletes a server; a server that is already gone is success.
func (c *Client) DeleteServer(ctx context.Context, id int64) error {
	return c.call(ctx, http.MethodDelete, serverPath(id, ""), nil, nil, nil, nil, true)
}

// ListServers returns every server matching p, following next_cursor until it
// is empty.
func (c *Client) ListServers(ctx context.Context, p ListServersParams) ([]Server, error) {
	var all []Server
	cursor := ""
	for {
		q := url.Values{"limit": {strconv.Itoa(listPageSize)}}
		if p.Status != "" {
			q.Set("status", p.Status)
		}
		if p.Query != "" {
			q.Set("q", p.Query)
		}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page listServersResponse
		if err := c.call(ctx, http.MethodGet, "/v1/servers", q, nil, nil, &page, false); err != nil {
			return nil, err
		}
		all = append(all, page.Data...)
		if page.NextCursor == "" {
			return all, nil
		}
		if page.NextCursor == cursor {
			return nil, errors.New("GET /v1/servers: the API returned the same cursor twice; stopping")
		}
		cursor = page.NextCursor
	}
}

// ListAlertTemplates returns the organization's alert templates.
func (c *Client) ListAlertTemplates(ctx context.Context) ([]AlertTemplate, error) {
	var out listTemplatesResponse
	if err := c.call(ctx, http.MethodGet, "/v1/alert-templates", nil, nil, nil, &out, false); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// ListNotificationChannels returns the organization's notification channels.
func (c *Client) ListNotificationChannels(ctx context.Context) ([]NotificationChannel, error) {
	var out listChannelsResponse
	if err := c.call(ctx, http.MethodGet, "/v1/notification-channels", nil, nil, nil, &out, false); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// SetServerAlertTemplate assigns a template to a server; templateID 0 means
// the organization's default template.
func (c *Client) SetServerAlertTemplate(ctx context.Context, id, templateID int64) error {
	return c.call(ctx, http.MethodPut, serverPath(id, "/alert-template"), nil, nil, setTemplateRequest{TemplateID: templateID}, nil, false)
}

// MuteServerAlerts mutes a server's alerts until the given time, or until
// lifted when until is nil.
func (c *Client) MuteServerAlerts(ctx context.Context, id int64, until *time.Time, reason string) (*MuteState, error) {
	req := muteRequest{Reason: reason}
	if until != nil {
		u := until.UTC()
		req.Until = &u
	}
	var out MuteState
	if err := c.call(ctx, http.MethodPost, serverPath(id, "/alerts:mute"), nil, nil, req, &out, false); err != nil {
		return nil, err
	}
	return &out, nil
}

// UnmuteServerAlerts lifts a mute; a server that does not exist is success.
func (c *Client) UnmuteServerAlerts(ctx context.Context, id int64) error {
	return c.call(ctx, http.MethodPost, serverPath(id, "/alerts:unmute"), nil, nil, nil, nil, true)
}
