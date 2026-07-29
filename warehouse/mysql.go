package warehouse

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	_ "github.com/go-sql-driver/mysql" // database/sql driver "mysql"
)

// OpenMySQL opens a MySQL/MariaDB warehouse.
//
// What MySQL can and cannot carry, stated up front: the semantic layer's own
// governance (metric-level RBAC, column masking, k-anonymity) is enforced in Go
// and works here unchanged. Database-level row security does not — MySQL has no
// RLS and no session GUCs for a policy to read — so QueryAs cannot propagate an
// end-user identity down to the data the way it does on Postgres. Configuring
// DI_DB_APP_ROLE against MySQL is therefore an error rather than a setting that
// appears to apply and quietly does nothing.
func OpenMySQL(ctx context.Context, dsn string, opts Options) (*Warehouse, error) {
	if opts.AppRole != "" {
		return nil, fmt.Errorf(
			"warehouse: DI_DB_APP_ROLE is set, but MySQL has no row-level security to enforce it — " +
				"unset it, or use Postgres if per-user row scoping must be enforced by the database")
	}
	native, err := MySQLDSN(dsn)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("mysql", native)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping warehouse: %w", err)
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	if opts.MaxRows <= 0 {
		opts.MaxRows = defaultMaxRows
	}
	return &Warehouse{db: db, opts: opts, driver: "mysql"}, nil
}

// MySQLDSN accepts either shape and returns what go-sql-driver/mysql wants:
//
//	mysql://user:pass@host:3306/db?tls=true   (URL — what a config file or a UI produces)
//	user:pass@tcp(host:3306)/db               (native — passed through untouched)
//
// parseTime is forced on: without it DATE/DATETIME arrive as []byte, and a
// bucket label that is bytes rather than a time sorts as text — '2024-9-1'
// after '2024-10-1'. That is a wrong answer that looks like a right one.
func MySQLDSN(dsn string) (string, error) {
	if !strings.Contains(dsn, "://") {
		return withParseTime(dsn), nil
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse mysql dsn: %w", err)
	}
	if u.Scheme != "mysql" && u.Scheme != "mariadb" {
		return "", fmt.Errorf("not a mysql dsn: scheme %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		host = "127.0.0.1"
	}
	port := u.Port()
	if port == "" {
		port = "3306"
	}
	var auth string
	if u.User != nil {
		if pw, ok := u.User.Password(); ok {
			auth = u.User.Username() + ":" + pw + "@"
		} else {
			auth = u.User.Username() + "@"
		}
	}
	native := fmt.Sprintf("%stcp(%s)/%s", auth, net(host, port), strings.TrimPrefix(u.Path, "/"))
	if q := u.RawQuery; q != "" {
		native += "?" + q
	}
	return withParseTime(native), nil
}

func net(host, port string) string {
	if strings.Contains(host, ":") { // IPv6 literal
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}

func withParseTime(dsn string) string {
	if strings.Contains(dsn, "parseTime=") {
		return dsn
	}
	if strings.Contains(dsn, "?") {
		return dsn + "&parseTime=true"
	}
	return dsn + "?parseTime=true"
}

// byteDecoder builds one converter per column.
//
// MySQL's wire protocol hands DECIMAL — and every SUM/AVG over it — back as
// []byte; SQL Server does the same for DECIMAL and MONEY. Left alone those
// marshal to JSON as base64: "MTIzNDUuNjc=" where a revenue figure should be.
// A governed answer would carry a number the caller cannot read and cannot tell
// is broken, which is precisely the silent-wrong-answer failure this whole layer
// exists to prevent. Conversion follows the declared column type rather than
// "does this look numeric", because a product code of "0012" is a string and
// has to stay one.
func byteDecoder(types []*sql.ColumnType) []func(any) any {
	out := make([]func(any) any, len(types))
	for i, ct := range types {
		switch strings.ToUpper(ct.DatabaseTypeName()) {
		case "DECIMAL", "NEWDECIMAL", "NUMERIC", "FLOAT", "DOUBLE", "REAL", "MONEY", "SMALLMONEY":
			out[i] = toFloat
		case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", "INTEGER", "BIGINT", "YEAR":
			out[i] = toInt
		default:
			out[i] = toText
		}
	}
	return out
}

func toFloat(v any) any {
	b, ok := v.([]byte)
	if !ok {
		return v
	}
	f, err := strconv.ParseFloat(string(b), 64)
	if err != nil {
		return string(b)
	}
	return f
}

func toInt(v any) any {
	b, ok := v.([]byte)
	if !ok {
		return v
	}
	n, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		return string(b)
	}
	return n
}

// toText leaves non-byte values alone; VARBINARY/BLOB reach here too, and a
// lossy UTF-8 reading beats base64 for anything a human is meant to read.
func toText(v any) any {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}
