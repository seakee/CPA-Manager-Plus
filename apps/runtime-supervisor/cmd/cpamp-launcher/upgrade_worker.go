//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

const upgradeWorkerSocket = "/run/cpamp-upgrade/worker.sock"

// configureUpgradeWorker admits only the script-prepared operation. The check
// precedes the central child reaper, so exec.Wait cannot race its Wait4 loop.
// A completed installation starts the normal three-process topology and does
// not leave a privileged migration listener or stale authentication proxy.
func configureUpgradeWorker(ctx context.Context, cfg *SupervisorConfig) error {
	env := parseEnvironMap(cfg.ManagerSpec.Env)
	socket := strings.TrimSpace(env["CPAMP_UPGRADE_WORKER_SOCKET"])
	if socket == "" {
		return nil
	}
	if socket != upgradeWorkerSocket {
		return errors.New("CPAMP_UPGRADE_WORKER_SOCKET must use the image-owned upgrade socket")
	}
	if strings.TrimSpace(env["CPAMP_UPGRADE_SOURCE_ROOT"]) == "" {
		return errors.New("the Docker upgrade source root is required")
	}

	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, cfg.ManagerSpec.Executable, "upgrade", "worker", "--check")
	cmd.Dir = cfg.ManagerSpec.Dir
	cmd.Env = cfg.ManagerSpec.Env
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("cannot inspect the prepared upgrade operation: %w", err)
	}
	required, err := decodeUpgradeWorkerCheck(output)
	if err != nil {
		return err
	}
	if !required {
		clean := make([]string, 0, len(cfg.ManagerSpec.Env))
		for _, item := range cfg.ManagerSpec.Env {
			if !strings.HasPrefix(item, "CPAMP_UPGRADE_") {
				clean = append(clean, item)
			}
		}
		cfg.ManagerSpec.Env = clean
		return nil
	}
	cfg.UpgradeSocket = socket

	cfg.UpgradeSpec = &ChildSpec{
		Name:       "upgrade-worker",
		Prefix:     "[upgrade-worker] ",
		Executable: cfg.ManagerSpec.Executable,
		Args:       []string{"upgrade", "worker", "--restart-parent"},
		Dir:        cfg.ManagerSpec.Dir,
		Env:        cfg.ManagerSpec.Env,
		UID:        RootUID,
		GID:        RootGID,
		ClearSupp:  true,
		HasUmask:   true,
		Umask:      0o077,
	}
	return nil
}

func decodeUpgradeWorkerCheck(data []byte) (bool, error) {
	var document struct {
		Required *bool `json:"required"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil || document.Required == nil {
		return false, errors.New("invalid prepared upgrade operation response")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return false, errors.New("invalid trailing prepared upgrade operation response")
	}
	return *document.Required, nil
}
