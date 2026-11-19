//go:build linux

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestA3PrivatePermitReaderFailsClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "permit.json")
	p := freshExternalPermit{Scope: freshExternalPrivateProfile, RunID: "private-a3-run-12345"}
	body, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	_, admitted := loadPrivateFreshExternalPermit(path)
	if admitted != (os.Geteuid() == 0) {
		t.Fatal("untrusted file owner was accepted or root-owned fixture rejected", admitted)
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadPrivateFreshExternalPermit(path); ok {
		t.Fatal("world-readable permit admitted")
	}
	if err = os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "alias.json")
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadPrivateFreshExternalPermit(link); ok {
		t.Fatal("symlink permit admitted")
	}
	if err = os.WriteFile(path, append(body, []byte(`{"injected":true}`)...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadPrivateFreshExternalPermit(path); ok {
		t.Fatal("trailing JSON admitted")
	}
	if err = os.WriteFile(path, []byte(`{"scope":"bogus","unknown":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadPrivateFreshExternalPermit(path); ok {
		t.Fatal("unknown permit fields admitted")
	}
}
