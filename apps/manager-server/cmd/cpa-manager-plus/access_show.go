package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/routing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshrouting"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const accessShowUsage = "Usage: cpa-manager-plus access show [--db-path PATH] [--origin URL]"

type savedAccessRoutes struct {
	control, inference, externalURL string
	external                        bool
	committed                       *freshrouting.State
}

func runAccess(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(stdout, accessShowUsage)
		return err
	}
	if len(args) == 0 || args[0] != "show" {
		return errors.New(accessShowUsage)
	}
	flags := flag.NewFlagSet("access show", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprintln(stderr, accessShowUsage); flags.PrintDefaults() }
	database := flags.String("db-path", "", "Manager SQLite path; defaults to the Manager configuration")
	origin := flags.String("origin", "", "public HTTP(S) origin; defaults to CPAMP_PUBLIC_ORIGIN")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New(accessShowUsage)
	}
	*origin = strings.TrimSpace(*origin)
	if *origin == "" {
		*origin = strings.TrimSpace(os.Getenv("CPAMP_PUBLIC_ORIGIN"))
	}
	if *origin != "" {
		canonical, err := normalizeAccessOrigin(*origin)
		if err != nil {
			return err
		}
		*origin = canonical
	}
	if *database == "" {
		cfg, err := config.LoadForInspection()
		if err != nil {
			return errors.New("cannot load Manager configuration; check the deployment or specify --db-path")
		}
		*database = cfg.DBPath
	}
	// Never open the live database in place: even read-only SQLite can touch its WAL index.
	if err := upgradeservice.ValidateScratchDir("", "", filepath.Dir(*database)); err != nil {
		return errors.New("the system temporary directory must be outside the Manager database directory")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	snapshot, err := upgradeservice.OpenSnapshotWithRetry(ctx, upgradeservice.OpenSnapshot, *database, "")
	if err != nil {
		return errors.New("cannot read a consistent Manager database snapshot; check the database path, permissions and temporary space")
	}
	defer snapshot.Close()
	saved, err := readSavedAccessRoutes(ctx, snapshot.DB())
	if err != nil {
		return errors.New("cannot verify the saved routing record")
	}
	if saved.control == "" {
		_, err = fmt.Fprintln(stdout, "Routing: no saved management prefix; finish installation or upgrade first.")
		return err
	}
	var active bool
	if saved.committed != nil {
		// The single-image launcher sets this only in the Manager child environment;
		// docker exec uses the same container's default loopback control listener.
		controlURL := strings.TrimSpace(os.Getenv("CPAMP_INGRESS_CONTROL_URL"))
		if controlURL == "" {
			controlURL = "http://127.0.0.1:9082"
		}
		if driver, err := freshrouting.NewHTTPDriver(freshrouting.HTTPConfig{IngressControlURL: controlURL}); err == nil {
			// A DB override may name an offline copy; equality includes owner and revision,
			// so a route from another installation cannot confirm this snapshot.
			liveCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			live, err := driver.Inspect(liveCtx)
			cancel()
			active = err == nil && live == *saved.committed
		}
	}
	return writeSavedAccessRoutes(stdout, saved, *origin, active)
}

const errInvalidAccessOriginMessage = "public origin must be an HTTP(S) origin without a path, query, fragment or credentials"

func normalizeAccessOrigin(raw string) (string, error) {
	canonical, err := cpa.NormalizeV8BaseURL(raw)
	if err != nil || strings.Count(canonical, "/") != 2 {
		return "", errors.New(errInvalidAccessOriginMessage)
	}
	parsed, err := url.Parse(canonical)
	if err != nil || parsed.Host == "" {
		return "", errors.New(errInvalidAccessOriginMessage)
	}
	hasExplicitPort := false
	if strings.HasPrefix(parsed.Host, "[") {
		hasExplicitPort = !strings.HasSuffix(parsed.Host, "]")
	} else {
		hasExplicitPort = strings.ContainsRune(parsed.Host, ':')
	}
	if hasExplicitPort {
		host, port, splitErr := net.SplitHostPort(parsed.Host)
		if splitErr != nil || host == "" || !validOriginPortNumber(port) {
			return "", errors.New(errInvalidAccessOriginMessage)
		}
	}
	return canonical, nil
}

func validOriginPortNumber(port string) bool {
	if len(port) == 0 || len(port) > 5 {
		return false
	}
	for i := range len(port) {
		if port[i] < '0' || port[i] > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535
}

func readSavedAccessRoutes(ctx context.Context, db *sql.DB) (savedAccessRoutes, error) {
	op, found, err := upgradeservice.ReadRecordFrom(ctx, db)
	if err != nil {
		return savedAccessRoutes{}, err
	}
	if found {
		repo := upgradeoperation.New(db)
		binding, bound, err := repo.ExternalBinding(ctx)
		if err != nil {
			return savedAccessRoutes{}, err
		}
		mode := upgradeservice.ObserveTargetMode(op, bound)
		saved := savedAccessRoutes{control: op.ControlBasePath, inference: op.InferenceBasePath, external: mode == model.UpgradeTargetExternal}
		if op.Stage == model.UpgradeStageComplete && upgradeOperationHasRoutingFields(op) {
			verified, evidence, ok, err := loadVerifiedCompletedUpgradeOperation(ctx, repo)
			if err != nil || !ok || validateCompletedUpgradeRoutingRow(verified, evidence.TargetMode) != nil {
				return savedAccessRoutes{}, errors.New("invalid completed upgrade routing")
			}
			projector := upgradeCanonicalServer{targetMode: evidence.TargetMode, sourceFingerprint: strings.TrimSpace(evidence.SourceFingerprint)}
			state := projector.stateFor(verified)
			saved.committed = &state
		}
		if saved.external {
			saved.externalURL = fencedUpgradeExternalBaseURL(ctx, db, op, binding, bound)
		}
		return saved, nil
	}
	r, err := freshstore.New(db).Load(ctx)
	if errors.Is(err, freshstore.ErrNotFresh) {
		return savedAccessRoutes{}, nil
	}
	if err != nil {
		return savedAccessRoutes{}, err
	}
	checkpoint := r.Operation.Routing
	saved := savedAccessRoutes{control: checkpoint.Target, inference: checkpoint.InferenceTarget, external: r.Operation.Strategy.Strategy == bootstrap.ConnectExternal}
	if saved.control == "" {
		saved.control, saved.inference = checkpoint.DraftControlBasePath, checkpoint.DraftInferenceBasePath
	}
	if checkpoint.Committed {
		state := freshrouting.StateFor(r.Operation)
		saved.committed = &state
	}
	if saved.external && isCommittedExternalStrategy(r.Operation.Strategy) && r.Connection != nil &&
		security.IsValidProtectedEnvelope(strings.TrimSpace(r.Connection.ManagementKey)) {
		base, err := cpa.NormalizeV8BaseURL(r.Connection.CPABaseURL)
		if err == nil && base == strings.TrimRight(r.Connection.CPABaseURL, "/") &&
			freshkeyapply.ExternalIdentity(base) == r.Operation.Strategy.TargetIdentity {
			saved.externalURL = base
		}
	}
	return saved, nil
}

func fencedUpgradeExternalBaseURL(
	ctx context.Context,
	db *sql.DB,
	op model.UpgradeOperation,
	binding upgradeoperation.ExternalBinding,
	bound bool,
) string {
	canonical := upgradeservice.FencedExternalBaseURLForStore(ctx, db, nil, op, binding, bound)
	if canonical == "" {
		return ""
	}
	st := store.New(db)
	managerCfg, managerOK, err := st.LoadManagerConfig(ctx)
	if err != nil {
		return ""
	}
	setupCfg, setupOK, err := st.LoadSetup(ctx)
	if err != nil {
		return ""
	}
	resolution, err := managerconfig.ResolveLegacyConnectionAuthority(managerCfg, managerOK, setupCfg, setupOK)
	if err != nil || resolution.Authority == managerconfig.LegacyConnectionAuthorityNone {
		return ""
	}
	if !security.IsValidProtectedEnvelope(strings.TrimSpace(resolution.Connection.ManagementKey)) {
		return ""
	}
	storedNorm, err := cpa.NormalizeV8BaseURL(strings.TrimSpace(resolution.Connection.BaseURL))
	if err != nil {
		return ""
	}
	storedCanon, err := identity.CanonicalExternalURL(storedNorm)
	if err != nil || storedCanon != canonical {
		return ""
	}
	return canonical
}

func writeSavedAccessRoutes(w io.Writer, saved savedAccessRoutes, origin string, active bool) error {
	if !bootstrap.ValidControlBasePath(saved.control) || routing.ReservedNamespaceCollision(saved.control) ||
		(saved.inference != "" && (!bootstrap.ValidInferenceBasePath(saved.inference) || routing.ReservedNamespaceCollision(saved.inference))) {
		return errors.New("saved access prefix is invalid")
	}
	// Raw paths are useful when the reverse proxy's public origin is not configured.
	var out strings.Builder
	status := "saved; not confirmed active"
	if active {
		status = "active in Ingress"
	}
	fmt.Fprintf(&out, "Management routing: %s\nManagement console: %s%s\nManagement API base: %s%s/api/v2\n", status, origin, saved.control, origin, saved.control)
	if saved.external {
		if saved.externalURL != "" {
			fmt.Fprintf(&out, "Configured External CPA base (direct): %s\n", saved.externalURL)
		} else {
			fmt.Fprintln(&out, "External CPA: use the directly reachable CPA address; no local inference prefix.")
		}
	} else if saved.inference != "" {
		inferenceStatus := "saved; not confirmed active"
		if active && saved.committed != nil && saved.committed.InferenceCommitted {
			inferenceStatus = "active in Ingress"
		}
		fmt.Fprintf(&out, "Inference routing: %s\nInference base: %s%s\n", inferenceStatus, origin, saved.inference)
	}
	if origin == "" {
		fmt.Fprintln(&out, "These are paths. Prepend your public origin or pass --origin.")
	}
	_, err := io.WriteString(w, out.String())
	return err
}
