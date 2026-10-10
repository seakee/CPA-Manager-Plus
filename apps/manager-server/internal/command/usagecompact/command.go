package usagecompact

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

const compactHeartbeatInterval = 30 * time.Second

type compactUsageFunc func(context.Context, string, sqlite.CompactProgressFunc) (sqlite.CompactResult, error)

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return runWithCompactorAndInterval(ctx, args, stdout, stderr, sqlite.CompactUsageWithProgress, compactHeartbeatInterval)
}

func runWithCompactor(ctx context.Context, args []string, stdout, stderr io.Writer, compact compactUsageFunc) error {
	return runWithCompactorAndInterval(ctx, args, stdout, stderr, compact, compactHeartbeatInterval)
}

func runWithCompactorAndInterval(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
	compact compactUsageFunc,
	heartbeatInterval time.Duration,
) (runErr error) {
	if compact == nil {
		return errors.New("compact-usage implementation is not configured")
	}
	flags := flag.NewFlagSet("compact-usage", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dbPath := flags.String("db-path", "", "path to the existing CPA Manager Plus usage.sqlite database")
	flags.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: cpa-manager-plus compact-usage [--db-path PATH]")
		_, _ = fmt.Fprintln(stderr, "Stop Manager Server before running this offline command.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("compact-usage does not accept positional arguments")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	path := strings.TrimSpace(*dbPath)
	if path == "" {
		cfg, err := config.LoadWithoutCreatingDefault()
		if err != nil {
			return fmt.Errorf("load config without creating defaults: %w", err)
		}
		path = cfg.DBPath
	}
	path, err := sqlite.ResolveMaintenancePath(path)
	if err != nil {
		return err
	}
	databaseLock, err := processlock.Acquire(path)
	if err != nil {
		if errors.Is(err, processlock.ErrLocked) {
			return fmt.Errorf("compact-usage requires exclusive database ownership; stop Manager Server and retry: %w", err)
		}
		return fmt.Errorf("acquire offline compact-usage database lock: %w", err)
	}
	lockClosed := false
	defer func() {
		if lockClosed {
			return
		}
		if err := databaseLock.Close(); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("release offline compact-usage database lock: %w", err))
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}

	reporter := newProgressReporter(stderr, heartbeatInterval)
	defer reporter.Stop()
	reporter.LogDatabaseOwnershipAcquired()

	result, err := compact(ctx, databaseLock.DatabasePath(), reporter.SetStage)
	reporter.Stop()
	if err != nil {
		return err
	}
	if err := databaseLock.Close(); err != nil {
		lockClosed = true
		return fmt.Errorf("release offline compact-usage database lock: %w", err)
	}
	lockClosed = true
	reporter.LogCompactionCompleted()

	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		return fmt.Errorf("write compact-usage result: %w", err)
	}
	return nil
}

type progressReporter struct {
	mu           sync.Mutex
	writer       io.Writer
	startedAt    time.Time
	stageStarted time.Time
	stage        sqlite.CompactStage

	ticker   *time.Ticker
	done     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func newProgressReporter(writer io.Writer, interval time.Duration) *progressReporter {
	now := time.Now()
	r := &progressReporter{
		writer:       writer,
		startedAt:    now,
		stageStarted: now,
		done:         make(chan struct{}),
	}
	if interval > 0 {
		r.ticker = time.NewTicker(interval)
		r.wg.Add(1)
		go r.heartbeatLoop()
	}
	return r
}

func (r *progressReporter) heartbeatLoop() {
	defer r.wg.Done()
	for {
		select {
		case <-r.done:
			return
		case now, ok := <-r.ticker.C:
			if !ok {
				return
			}
			r.mu.Lock()
			select {
			case <-r.done:
				r.mu.Unlock()
				return
			default:
			}
			if r.stage == "" {
				r.mu.Unlock()
				continue
			}
			stageElapsed := now.Sub(r.stageStarted).Round(time.Second)
			totalElapsed := now.Sub(r.startedAt).Round(time.Second)
			desc := stageDescription(r.stage)
			_, _ = fmt.Fprintf(r.writer, "[compact-usage] still running: %s (stage %s, total %s)\n", desc, stageElapsed, totalElapsed)
			r.mu.Unlock()
		}
	}
}

func (r *progressReporter) SetStage(stage sqlite.CompactStage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	select {
	case <-r.done:
		return
	default:
	}
	r.stage = stage
	r.stageStarted = time.Now()
	desc := stageDescription(stage)
	_, _ = fmt.Fprintf(r.writer, "[compact-usage] stage: %s\n", desc)
}

func (r *progressReporter) LogDatabaseOwnershipAcquired() {
	r.mu.Lock()
	defer r.mu.Unlock()
	select {
	case <-r.done:
		return
	default:
	}
	_, _ = fmt.Fprintln(r.writer, "[compact-usage] database ownership acquired")
}

func (r *progressReporter) LogCompactionCompleted() {
	r.mu.Lock()
	defer r.mu.Unlock()
	total := time.Since(r.startedAt).Round(time.Second)
	_, _ = fmt.Fprintf(r.writer, "[compact-usage] compaction completed (total %s)\n", total)
}

func (r *progressReporter) Stop() {
	r.stopOnce.Do(func() {
		r.mu.Lock()
		if r.ticker != nil {
			r.ticker.Stop()
		}
		close(r.done)
		r.mu.Unlock()
		r.wg.Wait()
	})
}

func stageDescription(stage sqlite.CompactStage) string {
	switch stage {
	case sqlite.CompactStagePrepare:
		return "preparing database"
	case sqlite.CompactStagePreflight:
		return "running preflight checks"
	case sqlite.CompactStageBaseline:
		return "reading baseline statistics"
	case sqlite.CompactStageCheckpoint:
		return "checkpointing WAL"
	case sqlite.CompactStageVacuum:
		return "vacuuming database"
	case sqlite.CompactStageVerify:
		return "verifying database integrity and logical consistency"
	case sqlite.CompactStageFinalize:
		return "reading final database statistics"
	default:
		return string(stage)
	}
}

func IsHelp(err error) bool {
	return errors.Is(err, flag.ErrHelp)
}
