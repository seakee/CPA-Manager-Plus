package cpaauthfiles

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
)

var ErrMutationCoordinatorUnavailable = errors.New("auth file mutation coordinator is unavailable")

// MutationCoordinator serializes CPA auth-file mutations by physical file.
// Mutations for different files may proceed concurrently, while AcquireAll is
// mutually exclusive with every file-scoped mutation.
type MutationCoordinator struct {
	mu         sync.Mutex
	active     map[string]struct{}
	activeAll  bool
	waitingAll int
	wait       chan struct{}
}

func NewMutationCoordinator() *MutationCoordinator {
	return &MutationCoordinator{
		active: make(map[string]struct{}),
		wait:   make(chan struct{}),
	}
}

func (c *MutationCoordinator) Acquire(ctx context.Context, fileNames ...string) (func(), error) {
	keys := mutationCoordinatorKeys(fileNames)
	if len(keys) == 0 {
		return func() {}, nil
	}
	return c.acquire(ctx, keys, false)
}

func (c *MutationCoordinator) AcquireAll(ctx context.Context) (func(), error) {
	return c.acquire(ctx, nil, true)
}

func (c *MutationCoordinator) acquire(ctx context.Context, keys []string, all bool) (func(), error) {
	if c == nil {
		return nil, ErrMutationCoordinatorUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	registeredAll := false
	for {
		if err := ctx.Err(); err != nil {
			if registeredAll {
				c.cancelAllWaiter()
			}
			return nil, err
		}

		c.mu.Lock()
		c.initializeLocked()
		if all && !registeredAll {
			c.waitingAll++
			registeredAll = true
		}
		if c.canAcquireLocked(keys, all) {
			if all {
				c.waitingAll--
				registeredAll = false
				c.activeAll = true
			} else {
				for _, key := range keys {
					c.active[key] = struct{}{}
				}
			}
			c.mu.Unlock()

			var once sync.Once
			return func() {
				once.Do(func() {
					c.mu.Lock()
					if all {
						c.activeAll = false
					} else {
						for _, key := range keys {
							delete(c.active, key)
						}
					}
					c.notifyLocked()
					c.mu.Unlock()
				})
			}, nil
		}
		wait := c.wait
		c.mu.Unlock()

		select {
		case <-ctx.Done():
			if registeredAll {
				c.cancelAllWaiter()
			}
			return nil, ctx.Err()
		case <-wait:
		}
	}
}

func (c *MutationCoordinator) initializeLocked() {
	if c.active == nil {
		c.active = make(map[string]struct{})
	}
	if c.wait == nil {
		c.wait = make(chan struct{})
	}
}

func (c *MutationCoordinator) cancelAllWaiter() {
	c.mu.Lock()
	if c.waitingAll > 0 {
		c.waitingAll--
	}
	c.notifyLocked()
	c.mu.Unlock()
}

func (c *MutationCoordinator) canAcquireLocked(keys []string, all bool) bool {
	if c.activeAll {
		return false
	}
	if all {
		return len(c.active) == 0
	}
	if c.waitingAll > 0 {
		return false
	}
	for _, key := range keys {
		if _, ok := c.active[key]; ok {
			return false
		}
	}
	return true
}

func (c *MutationCoordinator) notifyLocked() {
	c.initializeLocked()
	close(c.wait)
	c.wait = make(chan struct{})
}

func mutationCoordinatorKeys(fileNames []string) []string {
	seen := make(map[string]struct{}, len(fileNames))
	keys := make([]string, 0, len(fileNames))
	for _, fileName := range fileNames {
		key := strings.ToLower(strings.TrimSpace(fileName))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
