package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigKeepsStartExecutionOptional(t *testing.T) {
	values := validConfigValues()
	cfg, err := loadConfig(func(key string) string { return values[key] }, fixedGeneration(51))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.journalPath != "" || cfg.cpaExecutable != "" {
		t.Fatalf("read-only config unexpectedly enabled Start execution: %#v", cfg)
	}
}

func TestLoadConfigRequiresStartExecutionInputsTogether(t *testing.T) {
	for _, key := range []string{"CPAMP_RUNTIME_JOURNAL_PATH", "CPAMP_CPA_EXECUTABLE"} {
		t.Run(key, func(t *testing.T) {
			values := validConfigValues()
			values[key] = "configured"
			_, err := loadConfig(func(name string) string { return values[name] }, fixedGeneration(53))
			if err == nil || !strings.Contains(err.Error(), "must be configured together") {
				t.Fatalf("loadConfig() error = %v", err)
			}
		})
	}
}

func TestLoadConfigAcceptsPairedStartExecutionInputs(t *testing.T) {
	values := validConfigValues()
	values["CPAMP_RUNTIME_JOURNAL_PATH"] = " /runtime/operations.sqlite "
	values["CPAMP_CPA_EXECUTABLE"] = " /runtime/cpa "
	cfg, err := loadConfig(func(key string) string { return values[key] }, fixedGeneration(59))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.journalPath != "/runtime/operations.sqlite" || cfg.cpaExecutable != "/runtime/cpa" {
		t.Fatalf("Start execution config = %#v", cfg)
	}
}

func TestNewStartExecutorPreservesReadOnlyMode(t *testing.T) {
	cfg := loadTestConfig(t, fixedGeneration(61))
	start, store, err := newStartExecutor(context.Background(), cfg)
	if err != nil || start != nil || store != nil {
		t.Fatalf("newStartExecutor() = %v, %v, %v", start != nil, store != nil, err)
	}
}

func TestNewStartExecutorOpensSupervisorPrivateJournal(t *testing.T) {
	cfg := loadTestConfig(t, fixedGeneration(67))
	cfg.journalPath = filepath.Join(t.TempDir(), "runtime", "operations.sqlite")
	cfg.cpaExecutable = filepath.Join(t.TempDir(), "cpa")
	start, store, err := newStartExecutor(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if start == nil || store == nil {
		t.Fatal("Start execution was not configured")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}
