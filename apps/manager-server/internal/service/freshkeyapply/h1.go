package freshkeyapply

import (
	"context"
	"time"

	repo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

func (s *Service) EnableH1(h *cpa.H1Coordinator) {
	s = s.active()
	if s == nil {
		return
	}
	s.h1 = h
	s.wire.(*cpa.Transport).UseH1(h)
}
func (s *Service) finishH1(ctx context.Context, o *repo.Operation) error {
	if o.Outcome != "committed" || o.ParentID == "" {
		return cpa.ErrApplyUnknown
	}
	return s.h1.Finish(ctx, s.wire.(*cpa.Transport), o.ParentID, cpa.H1Terminal(o))
}
func (s *Service) continueH1(ctx context.Context, o *repo.Operation, n int, generation uint64) (OperationResult, error) {
	r := OperationResult{Operation: o, RuntimeState: "effect_unknown"}
	p, e := s.operationTarget(ctx, generation)
	if e != nil {
		return r, e
	}
	now := time.Now().UnixMilli()
	step := o.Steps[n]
	grant, e := s.keyPermit(ctx, o.ID, step.Name)
	if e != nil {
		return r, e
	}
	if grant.Version != 2 || grant.OperationID != o.ID || grant.ParentID != o.ParentID || grant.StepID != step.RuntimeID || grant.Step != step.Name || grant.RunID != o.RunID || grant.SourceID != o.SourceID || grant.Generation != o.Generation || grant.IssuedAtMS < s.startedAtMS || grant.IssuedAtMS < o.CreatedAtMS || grant.IssuedAtMS > now || grant.ExpiresAtMS <= now || grant.ExpiresAtMS-grant.IssuedAtMS > 60000 {
		return r, ErrUnavailable
	}
	t := s.wire.(*cpa.Transport)
	e = s.keyPair(ctx, o.TargetID, o.KeeperID, false, func(old, next string) error {
		before, after := []string{old, next}, []string{next}
		if step.Name == "add" {
			before, after = []string{old}, []string{old, next}
		}
		snap, err := t.KeySnapshot(ctx, p.ManagementKey)
		if err != nil {
			return err
		}
		if snap.SourceSHA256 != grant.SourceSHA256 || snap.SourceID != grant.SourceID || snap.Generation != grant.Generation || snap.Revision != grant.Revision || !sameKeys(snap.Keys, before) {
			return cpa.ErrSourceConflict
		}
		ok, err := t.SampleKeySet(ctx, before, nil)
		if err != nil {
			return err
		}
		if !ok {
			return cpa.ErrApplyUnknown
		}
		// Preserve survivor order instead of synthesizing a set order.
		if step.Name == "revoke" {
			after = []string{}
			for _, v := range snap.Keys {
				if v != old {
					after = append(after, v)
				}
			}
			if len(after) != 1 || after[0] != next {
				return ErrLastKey
			}
		}
		if !s.keyGrantHeld(ctx, grant, o.ID, step.Name) || time.Now().UnixMilli() >= grant.ExpiresAtMS {
			return ErrUnavailable
		}
		if _, err = s.target(ctx, p); err != nil {
			return err
		}
		snap, err = t.KeySnapshot(ctx, p.ManagementKey)
		if err != nil || snap.SourceSHA256 != grant.SourceSHA256 || snap.Revision != grant.Revision || !sameKeys(snap.Keys, before) {
			return cpa.ErrSourceConflict
		}
		o.Steps[n].State = "attempted"
		o.Outcome = "unknown"
		if err = s.repo.SaveOperation(ctx, *o); err != nil {
			return err
		}
		o.Revision++
		reserved, err := s.h1.Begin(ctx, t, o.ParentID, n)
		if err != nil {
			return err
		}
		o.Steps[n].BeginRevision = reserved.BeginRevision
		if !s.keyGrantHeld(ctx, grant, o.ID, step.Name) || time.Now().UnixMilli() >= grant.ExpiresAtMS {
			return ErrUnavailable
		}
		if err = s.h1.Reserved(ctx, t, o.ParentID, reserved); err != nil {
			return err
		}
		writeErr := t.WriteKeySet(ctx, p.ManagementKey, after)
		o.Steps[n].Returned = true
		o.Steps[n].HTTP200 = writeErr == nil
		saveCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err = s.repo.SaveOperation(saveCtx, *o); err != nil {
			return err
		}
		o.Revision++
		return writeErr
	})
	if e != nil {
		return r, e
	}
	return s.checkOperation(ctx, o, generation)
}
