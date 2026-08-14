package sqlite

import (
	"database/sql/driver"
	"fmt"
	"math"

	modernsqlite "modernc.org/sqlite"
)

const maxSQLiteInteger int64 = math.MaxInt64

type saturatingSumAggregate struct {
	total int64
	seen  bool
}

func init() {
	modernsqlite.MustRegisterDeterministicScalarFunction(
		"cpamp_saturating_add",
		2,
		func(_ *modernsqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			left, err := nonNegativeSQLiteInteger(args[0])
			if err != nil {
				return nil, err
			}
			right, err := nonNegativeSQLiteInteger(args[1])
			if err != nil {
				return nil, err
			}
			return saturatingSQLiteAdd(left, right), nil
		},
	)
	modernsqlite.MustRegisterFunction("cpamp_saturating_sum", &modernsqlite.FunctionImpl{
		NArgs:         1,
		Deterministic: true,
		MakeAggregate: func(modernsqlite.FunctionContext) (modernsqlite.AggregateFunction, error) {
			return &saturatingSumAggregate{}, nil
		},
	})
}

func (a *saturatingSumAggregate) Step(_ *modernsqlite.FunctionContext, args []driver.Value) error {
	if args[0] == nil {
		return nil
	}
	value, err := nonNegativeSQLiteInteger(args[0])
	if err != nil {
		return err
	}
	a.total = saturatingSQLiteAdd(a.total, value)
	a.seen = true
	return nil
}

func (a *saturatingSumAggregate) WindowInverse(_ *modernsqlite.FunctionContext, _ []driver.Value) error {
	return fmt.Errorf("cpamp_saturating_sum cannot be used as a window function")
}

func (a *saturatingSumAggregate) WindowValue(_ *modernsqlite.FunctionContext) (driver.Value, error) {
	if !a.seen {
		return nil, nil
	}
	return a.total, nil
}

func (a *saturatingSumAggregate) Final(_ *modernsqlite.FunctionContext) {}

func nonNegativeSQLiteInteger(value driver.Value) (int64, error) {
	switch value := value.(type) {
	case int64:
		if value < 0 {
			return 0, nil
		}
		return value, nil
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return 0, fmt.Errorf("token value is not a finite integer: %v", value)
		}
		if value <= 0 {
			return 0, nil
		}
		if value >= float64(maxSQLiteInteger) {
			return maxSQLiteInteger, nil
		}
		if math.Trunc(value) != value {
			return 0, fmt.Errorf("token value is not an integer: %v", value)
		}
		return int64(value), nil
	default:
		return 0, fmt.Errorf("token value has unsupported SQLite type %T", value)
	}
}

func saturatingSQLiteAdd(left, right int64) int64 {
	if left >= maxSQLiteInteger-right {
		return maxSQLiteInteger
	}
	return left + right
}
