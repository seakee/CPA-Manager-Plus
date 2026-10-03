package model

import (
	"strings"
	"time"
)

// Self-service reconnect: when a subscription login held by CPA can only
// recover through a new OAuth login, its owner (matched by the credential
// email) is sent a one-time link and reconnects it themselves.

const (
	ReconnectStatusPending   = "pending"
	ReconnectStatusCompleted = "completed"
	// ReconnectStatusResolved: the login works again without the link
	// (refresh recovered, or an admin reconnected it).
	ReconnectStatusResolved = "resolved"
	// ReconnectStatusExpired: an admin-sent link to someone without a login
	// ran out unused. Monitored requests never expire; reminders replace
	// their link on the same row.
	ReconnectStatusExpired = "expired"
)

// Why a request was opened.
const (
	ReconnectPurposeReconnect = "reconnect" // broken login, a real outage
	ReconnectPurposeTest      = "test"      // admin link, login works
	ReconnectPurposeInvite    = "invite"    // admin link, no login yet
)

// ReconnectRequest is one outage (or admin test/invite link) for one
// provider login of one person. The link token is stored only as a hash.
type ReconnectRequest struct {
	ID             int64  `json:"id"`
	Provider       string `json:"provider"`
	Email          string `json:"email"`
	AuthFileName   string `json:"authFileName,omitempty"`
	TokenHash      string `json:"-"`
	Status         string `json:"status"`
	Purpose        string `json:"purpose"`
	Manual         bool   `json:"manual"`
	Reason         string `json:"reason,omitempty"`
	OAuthState     string `json:"-"`
	OAuthStartedMS int64  `json:"-"`
	OAuthDeadline  int64  `json:"-"`
	KnownAuthFiles string `json:"-"`
	FollowupCount  int    `json:"followupCount"`
	CreatedAtMS    int64  `json:"createdAtMs"`
	ExpiresAtMS    int64  `json:"expiresAtMs"`
	NotifiedAtMS   int64  `json:"notifiedAtMs"`
	CompletedAtMS  int64  `json:"completedAtMs,omitempty"`
}

// ReconnectSettings is stored as one settings row. WebhookURL is encrypted
// at rest and never returned to the panel.
type ReconnectSettings struct {
	Enabled bool `json:"enabled"`
	// PublicURL is where people open the panel, used to build links
	// (e.g. https://cpamp.example.com).
	PublicURL string `json:"publicUrl"`
	// WebhookURL receives one POST per message: {sendTo, body, ...}.
	WebhookURL           string `json:"webhookUrl,omitempty"`
	SenderName           string `json:"senderName"`
	CheckIntervalMinutes int    `json:"checkIntervalMinutes"`
	LinkTTLHours         int    `json:"linkTtlHours"`
	FollowupHours        int    `json:"followupHours"`
	FollowupStartHour    int    `json:"followupStartHour"`
	FollowupEndHour      int    `json:"followupEndHour"`
	FollowupTimeZone     string `json:"followupTimeZone"`
	UpdatedAtMS          int64  `json:"updatedAtMs,omitempty"`
}

func DefaultReconnectSettings() ReconnectSettings {
	return ReconnectSettings{
		SenderName:           "CPA Manager Plus",
		CheckIntervalMinutes: 5,
		LinkTTLHours:         24,
		FollowupHours:        1,
		FollowupStartHour:    8,
		FollowupEndHour:      22,
		FollowupTimeZone:     "UTC",
	}
}

func clampReconnect(v, min, max, def int) int {
	if v < min || v > max {
		return def
	}
	return v
}

// Normalized applies defaults and limits: check interval 1-60 min, link
// lifetime 1-72 h, reminders every 1-6 h inside a valid From < Until window.
func (s ReconnectSettings) Normalized() ReconnectSettings {
	d := DefaultReconnectSettings()
	s.PublicURL = strings.TrimRight(strings.TrimSpace(s.PublicURL), "/")
	s.WebhookURL = strings.TrimSpace(s.WebhookURL)
	if strings.TrimSpace(s.SenderName) == "" {
		s.SenderName = d.SenderName
	}
	s.CheckIntervalMinutes = clampReconnect(s.CheckIntervalMinutes, 1, 60, d.CheckIntervalMinutes)
	s.LinkTTLHours = clampReconnect(s.LinkTTLHours, 1, 72, d.LinkTTLHours)
	s.FollowupHours = clampReconnect(s.FollowupHours, 1, 6, d.FollowupHours)
	if s.FollowupStartHour < 0 || s.FollowupStartHour > 23 || s.FollowupEndHour < 1 ||
		s.FollowupEndHour > 24 || s.FollowupStartHour >= s.FollowupEndHour {
		s.FollowupStartHour, s.FollowupEndHour = d.FollowupStartHour, d.FollowupEndHour
	}
	if _, err := time.LoadLocation(strings.TrimSpace(s.FollowupTimeZone)); err != nil || strings.TrimSpace(s.FollowupTimeZone) == "" {
		s.FollowupTimeZone = d.FollowupTimeZone
	}
	return s
}

// ReconnectOutage is one row of the panel's requests table.
type ReconnectOutage struct {
	ID              int64  `json:"id"`
	Provider        string `json:"provider"`
	Email           string `json:"email"`
	Status          string `json:"status"`
	Purpose         string `json:"purpose"`
	Manual          bool   `json:"manual"`
	Reason          string `json:"reason,omitempty"`
	FirstNotifiedMS int64  `json:"firstNotifiedAtMs"`
	LastMessageMS   int64  `json:"lastMessageAtMs"`
	Reminders       int    `json:"reminders"`
	ExpiresAtMS     int64  `json:"expiresAtMs"`
	ClosedAtMS      int64  `json:"closedAtMs,omitempty"`
}
