package sqlite

import (
	"fmt"
	"strings"
	"time"
)

const (
	defaultMaxOpenConns    = 4
	defaultMaxIdleConns    = 2
	defaultConnMaxIdleTime = 5 * time.Minute
)

type Options struct {
	Path                string
	DataSourceName      string
	ExpectedJournalMode string
	ExpectedSynchronous int
	ExpectedBusyTimeout int
	MaxOpenConns        int
	MaxIdleConns        int
	ConnMaxIdleTime     time.Duration
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

func (o Options) hasCustomDataSourceName() bool {
	return strings.TrimSpace(o.DataSourceName) != ""
}

func (o Options) validate() error {
	if !o.hasCustomDataSourceName() {
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(o.ExpectedJournalMode))
	if mode != "wal" && mode != "delete" {
		return fmt.Errorf("custom SQLite data source must declare expected journal mode WAL or DELETE")
	}
	if o.ExpectedSynchronous != 2 && o.ExpectedSynchronous != 3 {
		return fmt.Errorf("custom SQLite data source must declare expected synchronous mode FULL or EXTRA")
	}
	if o.ExpectedBusyTimeout <= 0 {
		return fmt.Errorf("custom SQLite data source must declare a positive expected busy timeout")
	}
	return nil
}
