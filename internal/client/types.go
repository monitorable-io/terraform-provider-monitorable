package client

import "time"

// Ids are JSON numbers on the wire; timestamps are RFC 3339 strings or null.

// Me is the API key making the request (GET /v1/me).
type Me struct {
	KeyID            int64      `json:"key_id"`
	KeyName          string     `json:"key_name"`
	Scopes           []string   `json:"scopes"`
	Role             string     `json:"role"`
	OrganizationID   int64      `json:"organization_id"`
	OrganizationName string     `json:"organization_name"`
	PlanName         string     `json:"plan_name"`
	ExpiresAt        *time.Time `json:"expires_at"`
}

// Server is a monitored server.
type Server struct {
	ID                int64      `json:"id"`
	Name              string     `json:"name"`
	Status            string     `json:"status"`
	Triage            string     `json:"triage"`
	LastSeenAt        *time.Time `json:"last_seen_at"`
	CreatedAt         *time.Time `json:"created_at"`
	IP                string     `json:"ip"`
	IPv4              string     `json:"ipv4"`
	IPv6              string     `json:"ipv6"`
	Location          string     `json:"location"`
	CountryCode       string     `json:"country_code"`
	AgentVersion      string     `json:"agent_version"`
	UptimeSeconds     float64    `json:"uptime_seconds"`
	AlertsMuted       bool       `json:"alerts_muted"`
	AlertsMutedUntil  *time.Time `json:"alerts_muted_until"`
	AlertsMutedReason string     `json:"alerts_muted_reason"`
	AlertTemplateID   int64      `json:"alert_template_id"`
	OpenIncidents     int64      `json:"open_incidents"`
	UnackedFiring     int64      `json:"unacked_firing"`
	CreatedVia        string     `json:"created_via"`
}

// CreateServerResponse is the answer to POST /v1/servers. AgentAPIKey and
// InstallCommand are "" on an Idempotency-Key replay (the fields are absent);
// Recovery is set only then.
type CreateServerResponse struct {
	Server         Server `json:"server"`
	AgentAPIKey    string `json:"agent_api_key"`
	InstallCommand string `json:"install_command"`
	Recovery       string `json:"recovery"`
}

// AlertTemplate is an organization alert template (the provider reads only
// its identity).
type AlertTemplate struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	IsDefault bool   `json:"is_default"`
}

// NotificationChannel is a notification channel; its settings are never
// returned by the API.
type NotificationChannel struct {
	ID      int64  `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// MuteState is a server's mute state.
type MuteState struct {
	ServerID int64      `json:"server_id"`
	Muted    bool       `json:"muted"`
	Until    *time.Time `json:"until"`
	Reason   string     `json:"reason"`
}

// ListServersParams filters GET /v1/servers.
type ListServersParams struct{ Status, Query string }

// Request and envelope bodies.

type createServerRequest struct {
	Name string `json:"name"`
}

type updateServerRequest struct {
	Name string `json:"name"`
}

type setTemplateRequest struct {
	TemplateID int64 `json:"template_id"`
}

type muteRequest struct {
	Until  *time.Time `json:"until,omitempty"`
	Reason string     `json:"reason,omitempty"`
}

type listServersResponse struct {
	Data       []Server `json:"data"`
	NextCursor string   `json:"next_cursor"`
}

type listTemplatesResponse struct {
	Data []AlertTemplate `json:"data"`
}

type listChannelsResponse struct {
	Data []NotificationChannel `json:"data"`
}
