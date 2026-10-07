package reconnect

import (
	"context"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

func location(settings model.ReconnectSettings) *time.Location {
	if loc, err := time.LoadLocation(settings.FollowupTimeZone); err == nil {
		return loc
	}
	return time.UTC
}

// inFollowupWindow reports whether reminders may be sent at now.
func inFollowupWindow(settings model.ReconnectSettings, now time.Time) bool {
	hour := now.In(location(settings)).Hour()
	return hour >= settings.FollowupStartHour && hour < settings.FollowupEndHour
}

// CheckDue reports whether a check is due by the configured interval.
func (s *Service) CheckDue(ctx context.Context) bool {
	settings, err := s.repo.LoadSettings(ctx)
	if err != nil || !settings.Enabled {
		return false
	}
	s.lastRunMSMu.Lock()
	defer s.lastRunMSMu.Unlock()
	return s.now().UnixMilli()-s.lastRunMS >= int64(settings.CheckIntervalMinutes)*int64(time.Minute/time.Millisecond)
}

// RunCheck performs one monitor pass: open requests for logins broken past
// the grace period, close requests whose login works again (with an
// all-clear message), send reminders, and close unused admin links.
func (s *Service) RunCheck(ctx context.Context) {
	if !s.running.TryLock() {
		return
	}
	defer s.running.Unlock()
	now := s.now()
	s.lastRunMSMu.Lock()
	s.lastRunMS = now.UnixMilli()
	s.lastRunMSMu.Unlock()

	settings, client, err := s.active(ctx)
	if err != nil {
		return
	}
	files, err := client.listAuthFiles(ctx)
	if err != nil {
		log.Printf("reconnect: list CPA auth files: %v", err)
		return
	}

	type brokenCred struct{ provider, email, name, reason string }
	broken := map[string]brokenCred{}
	s.firstSeenMu.Lock()
	seen := map[string]bool{}
	for _, f := range files {
		isBroken, reason := needsReauth(f)
		if !isBroken {
			continue
		}
		seen[f.Name] = true
		first, ok := s.firstSeen[f.Name]
		if !ok {
			s.firstSeen[f.Name] = now
			continue
		}
		if f.Email == "" || now.Sub(first) < grace {
			continue
		}
		key := ownerKey(f.Provider, f.Email)
		if _, dup := broken[key]; !dup {
			broken[key] = brokenCred{provider: f.Provider, email: f.Email, name: f.Name, reason: reason}
		}
	}
	for name := range s.firstSeen {
		if !seen[name] {
			delete(s.firstSeen, name)
		}
	}
	s.firstSeenMu.Unlock()

	stillBroken, hasLogin := map[string]bool{}, map[string]bool{}
	for _, f := range files {
		if !IsProvider(f.Provider) {
			continue
		}
		key := ownerKey(f.Provider, f.Email)
		hasLogin[key] = true
		if b, _ := needsReauth(f); b {
			stillBroken[key] = true
		}
	}

	if pending, err := s.repo.ListPending(ctx); err == nil {
		cleared := map[string]bool{}
		for i := range pending {
			req := &pending[i]
			key := ownerKey(req.Provider, req.Email)
			if stillBroken[key] || cleared[key] {
				continue
			}
			if req.Purpose == model.ReconnectPurposeTest || req.Purpose == model.ReconnectPurposeInvite {
				// Admin test/invite links close only when they run out unused:
				// recovered with a working login, not used without one.
				if now.UnixMilli() >= req.ExpiresAtMS {
					status := model.ReconnectStatusExpired
					if hasLogin[key] {
						status = model.ReconnectStatusResolved
					}
					_ = s.repo.Update(ctx, req.ID, map[string]any{"status": status, "completed_at_ms": now.UnixMilli()})
				}
				continue
			}
			// A real outage whose login works again: close it with an all-clear.
			cleared[key] = true
			_ = s.repo.ClosePending(ctx, req.Provider, req.Email, model.ReconnectStatusResolved, now.UnixMilli())
			if err := s.sendResolved(ctx, settings, req.Provider, req.Email); err != nil {
				log.Printf("reconnect: all-clear to %s: %v", req.Email, err)
			}
		}
	}

	followupEvery := time.Duration(settings.FollowupHours) * time.Hour
	for _, cred := range broken {
		open, err := s.repo.GetOpen(ctx, cred.provider, cred.email)
		if err != nil {
			continue
		}
		if open != nil {
			due := now.Sub(time.UnixMilli(open.NotifiedAtMS)) >= followupEvery
			expired := now.UnixMilli() >= open.ExpiresAtMS
			if (due || expired) && inFollowupWindow(settings, now) &&
				now.Sub(time.UnixMilli(open.OAuthStartedMS)) >= attemptInProgress {
				if err := s.followup(ctx, settings, open); err != nil {
					log.Printf("reconnect: reminder to %s: %v", cred.email, err)
				}
			}
			continue
		}
		if last, err := s.repo.LastCompletionMS(ctx, cred.provider, cred.email); err == nil && last > 0 &&
			now.Sub(time.UnixMilli(last)) < quietAfterCompletion {
			continue
		}
		if _, err := s.createAndNotify(ctx, settings, cred.provider, cred.email, cred.name, cred.reason, false, model.ReconnectPurposeReconnect); err != nil {
			log.Printf("reconnect: notify %s: %v", cred.email, err)
		}
	}
}

// Outages lists the last `days` of requests, open ones first.
func (s *Service) Outages(ctx context.Context, days int) ([]model.ReconnectOutage, error) {
	rows, err := s.repo.ListSince(ctx, s.now().AddDate(0, 0, -days).UnixMilli())
	if err != nil {
		return nil, err
	}
	out := make([]model.ReconnectOutage, 0, len(rows))
	for _, r := range rows {
		out = append(out, model.ReconnectOutage{
			ID: r.ID, Provider: providerFor(r.Provider).ID, Email: r.Email, Status: r.Status, Purpose: r.Purpose,
			Manual: r.Manual, Reason: r.Reason, FirstNotifiedMS: r.CreatedAtMS, LastMessageMS: r.NotifiedAtMS,
			Reminders: r.FollowupCount, ExpiresAtMS: r.ExpiresAtMS, ClosedAtMS: r.CompletedAtMS,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := out[i].Status == model.ReconnectStatusPending, out[j].Status == model.ReconnectStatusPending
		if pi != pj {
			return pi
		}
		return out[i].LastMessageMS > out[j].LastMessageMS
	})
	return out, nil
}

// ConnectionSummary counts supported logins per provider for the panel.
type ConnectionSummary struct {
	Provider string `json:"provider"`
	Logins   int    `json:"logins"`
	Broken   int    `json:"needingReconnect"`
}

func (s *Service) Summary(ctx context.Context) ([]ConnectionSummary, error) {
	setup, ok, err := s.setup.ResolveSetup(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNoSetup
	}
	client := &cpaClient{base: setup.CPAUpstreamURL, key: setup.ManagementKey, http: s.http, authFiles: s.authFiles}
	files, err := client.listAuthFiles(ctx)
	if err != nil {
		return nil, err
	}
	counts := map[string]*ConnectionSummary{}
	for _, id := range ProviderIDs {
		counts[id] = &ConnectionSummary{Provider: id}
	}
	for _, f := range files {
		c := counts[strings.ToLower(f.Provider)]
		if c == nil {
			continue
		}
		c.Logins++
		if b, _ := needsReauth(f); b {
			c.Broken++
		}
	}
	out := make([]ConnectionSummary, 0, len(ProviderIDs))
	for _, id := range ProviderIDs {
		out = append(out, *counts[id])
	}
	return out, nil
}
