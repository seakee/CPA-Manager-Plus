package update

import (
	"errors"
	"sync"
)

type ShutdownCoordinator struct {
	once sync.Once
	stop func()
	err  error
}

func NewShutdownCoordinator(stop func()) *ShutdownCoordinator {
	return &ShutdownCoordinator{stop: stop}
}

func (c *ShutdownCoordinator) RequestShutdown() error {
	if c == nil || c.stop == nil {
		return errors.New("managed shutdown is unavailable")
	}
	c.once.Do(c.stop)
	return c.err
}
