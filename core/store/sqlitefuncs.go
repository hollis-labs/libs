package store

import (
	"database/sql/driver"
	"strings"

	"modernc.org/sqlite"
)

// SQLiteLowerFunc is the name of the full-Unicode lower() the store registers
// with the SQLite driver. SQLite's built-in lower() folds ASCII only, so
// "CAFÉ" and "café" would not match.
const SQLiteLowerFunc = "gostore_lower"

func init() {
	// Registration is process-wide and idempotent in effect; the error can only
	// be a duplicate name, which means it is already there.
	_ = sqlite.RegisterDeterministicScalarFunction(SQLiteLowerFunc, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		switch v := args[0].(type) {
		case string:
			return strings.ToLower(v), nil
		}
		// BLOBs, numbers and NULL pass through unchanged.
		return args[0], nil
	})
}
