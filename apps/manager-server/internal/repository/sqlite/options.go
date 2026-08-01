package sqlite

import "time"

const (
	defaultMaxOpenConns    = 4
	defaultMaxIdleConns    = 2
	defaultConnMaxIdleTime = 5 * time.Minute
)

type Options struct {
	Path            string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxIdleTime time.Duration
}

func (o Options) maxOpenConns() int {
	if o.MaxOpenConns > 0 {
		return o.MaxOpenConns
	}
	return defaultMaxOpenConns
}

func (o Options) maxIdleConns() int {
	if o.MaxIdleConns > 0 {
		return o.MaxIdleConns
	}
	return defaultMaxIdleConns
}

func (o Options) connMaxIdleTime() time.Duration {
	if o.ConnMaxIdleTime > 0 {
		return o.ConnMaxIdleTime
	}
	return defaultConnMaxIdleTime
}
