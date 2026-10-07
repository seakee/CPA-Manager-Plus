package reconnect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

const (
	typeReconnect = "reconnect"
	typeFollowup  = "reconnect_followup"
	typeResolved  = "reconnect_resolved"
	typeTest      = "reconnect_test"
	typeInvite    = "reconnect_invite"
	// typeWelcome: someone's new login was added from another person's link
	// (they signed in with this account by mistake).
	typeWelcome = "reconnect_welcome"
)

// webhookPayload is POSTed to the notification webhook. SendTo and Body are
// enough to deliver a ready-made message (e.g. a Microsoft Teams Power
// Automate flow or a Slack workflow); the other fields are for receivers that
// build their own message.
type webhookPayload struct {
	SendTo    string `json:"sendTo"`
	Body      string `json:"body"`
	Type      string `json:"type"`
	Provider  string `json:"provider"`
	Email     string `json:"email"`
	Link      string `json:"link,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Followup  int    `json:"followup"`
}

func (s *Service) send(ctx context.Context, settings model.ReconnectSettings, p webhookPayload) error {
	if strings.TrimSpace(settings.WebhookURL) == "" {
		return ErrNoWebhook
	}
	p.SendTo = p.Email
	p.Body = messageBody(settings.SenderName, p)
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, settings.WebhookURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("notification webhook: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return errors.New("notification webhook: HTTP " + resp.Status + " " + strings.TrimSpace(string(snippet)))
	}
	return nil
}

// messageBody renders the message in simple HTML.
func messageBody(sender string, p webhookPayload) string {
	site := html.EscapeString(sender)
	name := html.EscapeString(providerFor(p.Provider).Name)
	link := html.EscapeString(p.Link)
	expires := p.ExpiresAt
	if t, err := time.Parse(time.RFC3339, p.ExpiresAt); err == nil {
		expires = t.UTC().Format("Mon 2 Jan 15:04 UTC")
	}
	footer := "<br><br>The link works once and expires " + html.EscapeString(expires) + "."
	switch p.Type {
	case typeResolved:
		return "🤖 | ✅ <b>Your " + name + " login is working again</b><br><br>Your " + name +
			" account is connected to " + site + " again, so <b>no action is needed</b>. You can ignore the earlier reconnect links."
	case typeWelcome:
		return "🤖 | 🎉 <b>Welcome aboard: your " + name + " account is connected to " + site + "</b><br><br>Your " + name +
			" subscription is now connected to " + site + " and <b>contributes to the team's shared capacity</b>. Nothing else is needed — thank you!"
	case typeTest:
		return "🤖 | 🧪 <b>Test: reconnect your " + name + " account to " + site + "</b><br><br>This is a test message from your " +
			site + " admin. Your " + name + " login is working, so nothing is required. You can use the link to reconnect it, or ignore this message." +
			"<br><br>👉 <a href=\"" + link + "\">Reconnect " + name + "</a> (takes about 1 minute)" + footer
	case typeInvite:
		return "🤖 | 🔗 <b>Invitation: connect your " + name + " account to " + site + "</b><br><br>You're invited to connect your " +
			name + " subscription to " + site + " so it joins the team's shared capacity." +
			"<br><br>👉 <b><a href=\"" + link + "\">Connect " + name + " now</a></b> (takes about 1 minute)" + footer
	}
	headline := "🤖 | ⚠️ <b>Action required: your " + name + " login is disconnected</b>"
	impact := "Your " + name + " account is no longer connected to " + site + ", so <b>your subscription has stopped contributing to the team's shared capacity</b>. " +
		"Your requests are now being served by your teammates' accounts."
	if p.Followup > 0 {
		headline = fmt.Sprintf("🤖 | 🔴 <b>Reminder #%d: your %s login is still disconnected</b>", p.Followup, name)
		impact = "Your " + name + " account is still disconnected from " + site + ", so <b>your usage keeps landing on your teammates' accounts</b>. " +
			"Earlier reconnect links no longer work — use the one below."
	}
	return headline + "<br><br>" + impact + "<br><br>👉 <b><a href=\"" + link + "\">Reconnect " + name + " now</a></b> (takes about 1 minute)" + footer
}

// sendResolved tells the owner their login works again; it carries no link.
func (s *Service) sendResolved(ctx context.Context, settings model.ReconnectSettings, providerID, email string) error {
	return s.send(ctx, settings, webhookPayload{Type: typeResolved, Provider: providerID, Email: email})
}
