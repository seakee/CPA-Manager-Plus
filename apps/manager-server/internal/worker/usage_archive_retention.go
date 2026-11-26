package worker

import (
	"context"
	"errors"
	"log"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	usagearchive "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagearchive"
	usageservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const (
	defaultUsageArchiveRetentionInterval      = 24 * time.Hour
	defaultUsageArchiveRetentionRetryInterval = 30 * time.Second
)

type UsageArchiveRetentionWorker struct {
	service          usageArchiveRetentionService
	retentionDays    int
	settingsResolver func(context.Context) (bool, int, error)
	wakeCh           chan struct{}
	interval         time.Duration
	retryInterval    time.Duration
	running          atomic.Int32
	lifecycleMu      sync.Mutex
	cancel           context.CancelFunc
	done             chan struct{}
	stopped          bool
	now              func() time.Time
}
type usageArchiveRetentionService interface {
	ActiveArchiveRun(context.Context) (store.UsageArchiveRun, bool, error)
	CreateRetentionArchive(context.Context, int64) (usageservice.ArchiveStatus, error)
	ResumeArchive(context.Context, string) (usageservice.ArchiveStatus, error)
	VerifyArchive(context.Context, string) (usageservice.ArchiveStatus, error)
	DeleteArchive(context.Context, string) (usageservice.ArchiveStatus, error)
}

type usageArchiveCanceller interface {
	CancelArchive(context.Context, string) (usageservice.ArchiveStatus, error)
}

func NewUsageArchiveRetentionWorker(service usageArchiveRetentionService, retentionDays int) *UsageArchiveRetentionWorker {
	return &UsageArchiveRetentionWorker{
		service:       service,
		retentionDays: retentionDays,
		wakeCh:        make(chan struct{}, 1),
		interval:      defaultUsageArchiveRetentionInterval,
		retryInterval: defaultUsageArchiveRetentionRetryInterval,
		now:           time.Now,
	}
}

func (w *UsageArchiveRetentionWorker) SetSettingsResolver(resolver func(context.Context) (bool, int, error)) {
	if w != nil {
		w.settingsResolver = resolver
	}
}

func (w *UsageArchiveRetentionWorker) Wake() {
	if w == nil || w.wakeCh == nil {
		return
	}
	select {
	case w.wakeCh <- struct{}{}:
	default:
	}
}

func (w *UsageArchiveRetentionWorker) Start(ctx context.Context) {
	if w == nil || w.service == nil || (w.retentionDays <= 0 && w.settingsResolver == nil) {
		return
	}
	if ctx == nil || ctx.Err() != nil {
		return
	}
	w.lifecycleMu.Lock()
	defer w.lifecycleMu.Unlock()
	if w.done != nil || w.stopped || ctx.Err() != nil {
		return
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	w.cancel = cancel
	w.done = done
	go func() {
		defer close(done)
		defer cancel()
		w.loop(workerCtx)
	}()
}

// StopAndWait fences delayed startup, cancels retention work, and waits for
// archive-file and database cleanup before the shared store is closed.
func (w *UsageArchiveRetentionWorker) StopAndWait(ctx context.Context) error {
	if w == nil {
		return nil
	}
	w.lifecycleMu.Lock()
	w.stopped = true
	cancel, done := w.cancel, w.done
	w.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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
		case <-w.wakeCh:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// runOnce returns true when an active automatic run should be retried before
// the normal daily interval. This keeps a transient derived-coverage failure
// from holding the maintenance lock for an entire retention period.
func (w *UsageArchiveRetentionWorker) runOnce(ctx context.Context) bool {
	if w == nil || w.service == nil {
		return false
	}
	done, blocked := middleware.BeginBackgroundMutation()
	if blocked {
		return ctx.Err() == nil
	}
	defer done()
	retentionDays := w.retentionDays
	if w.settingsResolver != nil {
		enabled, resolvedDays, resolveErr := w.settingsResolver(ctx)
		if resolveErr != nil {
			log.Printf("[usage-retention] resolve retention config: %v", resolveErr)
			return ctx.Err() == nil
		}
		if !enabled || resolvedDays <= 0 {
			w.cleanupDisabledRetentionRun(ctx)
			return false
		}
		retentionDays = resolvedDays
	} else if retentionDays <= 0 {
		w.cleanupDisabledRetentionRun(ctx)
		return false
	}
	if !w.running.CompareAndSwap(0, 1) {
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
			if errors.Is(err, usageservice.ErrArchiveUnrestorableEventHash) {
				return false
			}
			return ctx.Err() == nil
		}
		return false
	}

	now := time.Now
	if w.now != nil {
		now = w.now
	}
	days64 := int64(retentionDays)
	if days64 > math.MaxInt64/int64(24*time.Hour) {
		log.Printf("[usage-retention] retention days %d exceeds the supported duration", retentionDays)
		return false
	}
	retentionDuration := time.Duration(days64 * int64(24*time.Hour))
	cutoffMS := now().Add(-retentionDuration).UnixMilli()
	if cutoffMS <= 0 {
		log.Printf("[usage-retention] retention days %d places the cutoff before the supported timestamp range", retentionDays)
		return false
	}
	status, err := w.service.CreateRetentionArchive(ctx, cutoffMS)
	if err != nil {
		if !errors.Is(err, usagearchive.ErrNoEvents) && !errors.Is(err, usagearchive.ErrMaintenanceLocked) {
			log.Printf("[usage-retention] create retention archive: %v", err)
		}
		if errors.Is(err, usageservice.ErrArchiveUnrestorableEventHash) {
			return false
		}
		return ctx.Err() == nil &&
			!errors.Is(err, usagearchive.ErrNoEvents)
	}
	if err := w.advance(ctx, status.Run.ID); err != nil {
		log.Printf("[usage-retention] process retention archive %s: %v", status.Run.ID, err)
		if errors.Is(err, usageservice.ErrArchiveUnrestorableEventHash) {
			return false
		}
		return ctx.Err() == nil
	}
	return false
}

func (w *UsageArchiveRetentionWorker) cleanupDisabledRetentionRun(ctx context.Context) {
	if w == nil || w.service == nil {
		return
	}
	canceller, ok := w.service.(usageArchiveCanceller)
	if !ok {
		return
	}
	if !w.running.CompareAndSwap(0, 1) {
		return
	}
	defer w.running.Store(0)
	active, found, err := w.service.ActiveArchiveRun(ctx)
	if err != nil {
		log.Printf("[usage-retention] inspect active archive run while disabled: %v", err)
		return
	}
	if !found || active.Mode != usagearchive.RunModeRetention {
		return
	}
	if _, cancelErr := canceller.CancelArchive(ctx, active.ID); cancelErr != nil {
		if errors.Is(cancelErr, usageservice.ErrArchiveCancelPublished) ||
			errors.Is(cancelErr, usageservice.ErrArchiveCancelUnsafe) ||
			errors.Is(cancelErr, usageservice.ErrArchiveInvalidState) {
			if advErr := w.advance(ctx, active.ID); advErr != nil {
				log.Printf("[usage-retention] settle active archive run %s while disabled: %v", active.ID, advErr)
			}
			return
		}
		log.Printf("[usage-retention] cancel active archive run %s while disabled: %v", active.ID, cancelErr)
	}
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
