package repo

import (
	"errors"
	"strconv"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"sync/atomic"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Bot leases, send windows, retry times and route expiry are stored as epoch
// microseconds (BIGINT, *_us columns) and are both written and compared with
// the database clock inside SQL. Go wall clocks never decide ownership, so
// replicas with skewed clocks cannot steal or keep a lease they should not.
//
// MySQL: UNIX_TIMESTAMP(NOW(6)) is independent of the session time zone (NOW
// and UNIX_TIMESTAMP use the same zone) and honours SET timestamp in tests.
// SQLite: julianday('now') in microseconds; precision is about one
// millisecond, far below any lease or window used here.
const (
	mysqlNowUS  = "CAST(UNIX_TIMESTAMP(NOW(6)) * 1000000 AS SIGNED)"
	sqliteNowUS = "CAST(ROUND((julianday('now') - 2440587.5) * 86400000000.0) AS INTEGER)"
)

// dbClockOverrideUS pins the database clock for tests; 0 means the real
// clock. Only tests in this package set it.
var dbClockOverrideUS atomic.Int64

// dbNowUSSQL returns the SQL expression for "now" in epoch microseconds.
func (d *deps) dbNowUSSQL() string {
	if v := dbClockOverrideUS.Load(); v != 0 {
		return strconv.FormatInt(v, 10)
	}
	if d.isMySQL() {
		return mysqlNowUS
	}
	return sqliteNowUS
}

// nowUSExpr is dbNowUSSQL as a gorm expression, optionally offset.
func (d *deps) nowUSExpr(offsetUS int64) clause.Expr {
	if offsetUS == 0 {
		return gorm.Expr(d.dbNowUSSQL())
	}
	return gorm.Expr("("+d.dbNowUSSQL()+" + ?)", offsetUS)
}

// dbNowUS reads the database clock.
func (d *deps) dbNowUS(tx *gorm.DB) (int64, error) {
	var now int64
	err := tx.Raw("SELECT " + d.dbNowUSSQL()).Scan(&now).Error
	return now, err
}

// retryBotDeadlock reruns a transaction that MySQL rolled back as a deadlock
// victim (error 1213), for example two creates locking the same missing index
// gap. Only that explicit error is retried: the whole transaction was undone.
func retryBotDeadlock(transaction func() error) error {
	var err error
	for attempt := 0; attempt < 6; attempt++ {
		err = transaction()
		var sqlErr *mysqldriver.MySQLError
		if !errors.As(err, &sqlErr) || sqlErr.Number != 1213 || attempt == 5 {
			return err
		}
		time.Sleep(time.Duration(1<<attempt) * 5 * time.Millisecond)
	}
	return err
}
