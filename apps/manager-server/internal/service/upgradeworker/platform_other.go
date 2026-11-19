//go:build !linux

package upgradeworker

import (
	"context"
	"errors"
	"net/http"
	"os"
	"reflect"
	"time"
)

type peerState struct {
	enforced bool
	allowed  bool
}

type peerContextKey struct{}

func fileLinkCount(info os.FileInfo) uint64 {
	if info == nil || info.Sys() == nil {
		return 1
	}
	val := reflect.ValueOf(info.Sys())
	if val.Kind() == reflect.Pointer {
		if val.IsNil() {
			return 1
		}
		val = val.Elem()
	}
	if val.Kind() != reflect.Struct {
		return 1
	}
	field := val.FieldByName("Nlink")
	if !field.IsValid() {
		return 1
	}
	switch field.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return field.Uint()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if field.Int() > 0 {
			return uint64(field.Int())
		}
	}
	return 1
}

func peerAllowed(ctx context.Context) bool {
	state, ok := ctx.Value(peerContextKey{}).(peerState)
	if !ok || !state.enforced {
		return true
	}
	return state.allowed
}

func withSocketPeerEnforcement(ctx context.Context, allowed bool) context.Context {
	return context.WithValue(ctx, peerContextKey{}, peerState{enforced: true, allowed: allowed})
}

func Serve(context.Context, string, http.Handler) error {
	return errors.New("upgrade_worker_requires_linux_docker")
}

func RepairTargetOwnership(targetRoot string) error {
	if err := walkTargetOwnership(targetRoot, nil); err != nil {
		return err
	}
	return errors.New("upgrade_worker_requires_linux_docker")
}

type ParentRestart struct {
	Validate func() error
	Restart  func() error
	Close    func()
}

func BindLauncherParentRestart(time.Duration) (ParentRestart, error) {
	return ParentRestart{}, errors.New("upgrade_worker_requires_linux_docker")
}
