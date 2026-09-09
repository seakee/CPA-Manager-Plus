package worker

import (
	"context"
	"errors"
	"log"
	"math"
	"sync"
	"sync/atomic"
	"time"

	usagearchive "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagearchive"
	usageservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const (
	defaultUsageArchiveRetentionInterval      = 24 * time.Hour
	defaultUsageArchiveRetentionRetryInterval = 30 * time.Second
)

type UsageArchiveRetentionWorker struct {
	service       usageArchiveRetentionService
	retentionDays int
	interval      time.Duration
	retryInterval time.Duration
	running       atomic.Int32
	start         sync.Once
	now           func() time.Time
}

type usageArchiveRetentionService interface {
	ActiveArchiveRun(context.Context) (store.UsageArchiveRun, bool, error)
	CreateRetentionArchive(context.Context, int64) (usageservice.ArchiveStatus, error)
	ResumeArchive(context.Context, string) (usageservice.ArchiveStatus, error)
	VerifyArchive(context.Context, string) (usageservice.ArchiveStatus, error)
	DeleteArchive(context.Context, string) (usageservice.ArchiveStatus, error)
}

func NewUsageArchiveRetentionWorker(service usageArchiveRetentionService, retentionDays int) *UsageArchiveRetentionWorker {
	return &UsageArchiveRetentionWorker{
		service:       service,
		retentionDays: retentionDays,
		interval:      defaultUsageArchiveRetentionInterval,
		retryInterval: defaultUsageArchiveRetentionRetryInterval,
		now:           time.Now,
	}
}

func (w *UsageArchiveRetentionWorker) Start(ctx context.Context) {
	if w == nil || w.service == nil || w.retentionDays <= 0 {
		return
	}
	w.start.Do(func() {
		go w.loop(ctx)
	})
}

func (w *UsageArchiveRetentionWorker) loop(ctx context.Context) {
	for {
		delay := w.interval
		if delay <= 0 {
			delay = defaultUsageArchiveRetentionInterval
		}
		if w.runOnce(ctx) {
			delay = w.retryInterval
			if delay <= 0 {
				delay = defaultUsageArchiveRetentionRetryInterval
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// runOnce returns true when an active automatic run should be retried before
// the normal daily interval. This keeps a transient derived-coverage failure
// from holding the maintenance lock for an entire retention period.
func (w *UsageArchiveRetentionWorker) runOnce(ctx context.Context) bool {
	if w == nil || w.service == nil || w.retentionDays <= 0 || !w.running.CompareAndSwap(0, 1) {
		return false
	}
	defer w.running.Store(0)

	active, found, err := w.service.ActiveArchiveRun(ctx)
	if err != nil {
		log.Printf("[usage-retention] inspect active archive run: %v", err)
		return ctx.Err() == nil
	}
	if found {
		if active.Mode != usagearchive.RunModeRetention {
			log.Printf("[usage-retention] manual archive run %s is active; automatic retention will wait", active.ID)
			return ctx.Err() == nil
		}
		if err := w.advance(ctx, active.ID); err != nil {
			log.Printf("[usage-retention] resume archive run %s: %v", active.ID, err)
			return ctx.Err() == nil
		}
		return false
	}

	now := time.Now
	if w.now != nil {
		now = w.now
	}
	retentionDays := int64(w.retentionDays)
	if retentionDays > math.MaxInt64/int64(24*time.Hour) {
		log.Printf("[usage-retention] retention days %d exceeds the supported duration", w.retentionDays)
		return false
	}
	retentionDuration := time.Duration(retentionDays * int64(24*time.Hour))
	cutoffMS := now().Add(-retentionDuration).UnixMilli()
	if cutoffMS <= 0 {
		log.Printf("[usage-retention] retention days %d places the cutoff before the supported timestamp range", w.retentionDays)
		return false
	}
	status, err := w.service.CreateRetentionArchive(ctx, cutoffMS)
	if err != nil {
		if !errors.Is(err, usagearchive.ErrNoEvents) && !errors.Is(err, usagearchive.ErrMaintenanceLocked) {
			log.Printf("[usage-retention] create retention archive: %v", err)
		}
		return ctx.Err() == nil &&
			!errors.Is(err, usagearchive.ErrNoEvents)
	}
	if err := w.advance(ctx, status.Run.ID); err != nil {
		log.Printf("[usage-retention] process retention archive %s: %v", status.Run.ID, err)
		return ctx.Err() == nil
	}
	return false
}

func (w *UsageArchiveRetentionWorker) advance(ctx context.Context, runID string) error {
	for step := 0; step < 8; step++ {
		status, err := w.service.ResumeArchive(ctx, runID)
		if err != nil {
			return err
		}
		switch status.Run.Status {
		case usagearchive.StatusArchived:
			if _, err := w.service.VerifyArchive(ctx, runID); err != nil {
				return err
			}
		case usagearchive.StatusVerified:
			if _, err := w.service.DeleteArchive(ctx, runID); err != nil {
				return err
			}
		case usagearchive.StatusCompleted:
			return nil
		case usagearchive.StatusFailed:
			return errors.New("retention archive remains failed after resume")
		default:
			continue
		}
	}
	return errors.New("retention archive did not reach completed state")
}
