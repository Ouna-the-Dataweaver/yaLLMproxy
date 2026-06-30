package logdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type databaseDialect struct {
	name       string
	driverName string
}

var (
	sqliteDialect   = databaseDialect{name: "sqlite", driverName: "sqlite"}
	postgresDialect = databaseDialect{name: "postgres", driverName: "pgx"}
)

func databaseBackend(raw map[string]any) (databaseDialect, error) {
	backend := strings.ToLower(strings.TrimSpace(fmt.Sprint(raw["backend"])))
	if backend == "" || backend == "<nil>" {
		return sqliteDialect, nil
	}
	switch backend {
	case "sqlite":
		return sqliteDialect, nil
	case "postgres", "postgresql":
		return postgresDialect, nil
	default:
		return databaseDialect{}, fmt.Errorf("unsupported database backend %q", backend)
	}
}

func (d databaseDialect) bind(query string) string {
	if d.name != "postgres" {
		return query
	}
	return rebindQuestionPlaceholders(query)
}

func (d databaseDialect) placeholder(index int) string {
	if d.name == "postgres" {
		return "$" + strconv.Itoa(index)
	}
	return "?"
}

func (s *Store) migrateRequestMetadataColumns(ctx context.Context) error {
	required := []struct {
		name string
		decl string
	}{
		{name: "trace_id", decl: "TEXT"},
		{name: "span_id", decl: "TEXT"},
	}
	existing, err := s.requestMetadataColumns(ctx)
	if err != nil {
		return err
	}
	for _, column := range required {
		if existing[column.name] {
			continue
		}
		stmt := fmt.Sprintf("ALTER TABLE request_metadata ADD COLUMN %s %s", column.name, column.decl)
		if _, err := s.execContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate request_metadata.%s: %w", column.name, err)
		}
		existing[column.name] = true
	}
	if _, err := s.execContext(ctx, `CREATE INDEX IF NOT EXISTS idx_request_metadata_trace_id ON request_metadata(trace_id)`); err != nil {
		return fmt.Errorf("create request_metadata.trace_id index: %w", err)
	}
	return nil
}

func (s *Store) requestMetadataColumns(ctx context.Context) (map[string]bool, error) {
	switch s.dialect.name {
	case "sqlite":
		return s.sqliteRequestMetadataColumns(ctx)
	case "postgres":
		return s.postgresRequestMetadataColumns(ctx)
	default:
		return nil, fmt.Errorf("unsupported database backend %q", s.dialect.name)
	}
}

func (s *Store) sqliteRequestMetadataColumns(ctx context.Context) (map[string]bool, error) {
	rows, err := s.queryContext(ctx, "PRAGMA table_info(request_metadata)")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue sql.NullString
		var primaryKey int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		out[strings.ToLower(name)] = true
	}
	return out, rows.Err()
}

func (s *Store) postgresRequestMetadataColumns(ctx context.Context) (map[string]bool, error) {
	rows, err := s.queryContext(ctx, `SELECT column_name
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'request_metadata'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[strings.ToLower(name)] = true
	}
	return out, rows.Err()
}

func rebindQuestionPlaceholders(query string) string {
	var out strings.Builder
	out.Grow(len(query) + 8)
	index := 1
	inSingle := false
	inDouble := false
	inLineComment := false
	inBlockComment := false
	for i := 0; i < len(query); i++ {
		ch := query[i]
		next := byte(0)
		if i+1 < len(query) {
			next = query[i+1]
		}
		switch {
		case inLineComment:
			out.WriteByte(ch)
			if ch == '\n' {
				inLineComment = false
			}
		case inBlockComment:
			out.WriteByte(ch)
			if ch == '*' && next == '/' {
				out.WriteByte(next)
				i++
				inBlockComment = false
			}
		case inSingle:
			out.WriteByte(ch)
			if ch == '\'' {
				if next == '\'' {
					out.WriteByte(next)
					i++
				} else {
					inSingle = false
				}
			}
		case inDouble:
			out.WriteByte(ch)
			if ch == '"' {
				if next == '"' {
					out.WriteByte(next)
					i++
				} else {
					inDouble = false
				}
			}
		case ch == '\'':
			out.WriteByte(ch)
			inSingle = true
		case ch == '"':
			out.WriteByte(ch)
			inDouble = true
		case ch == '-' && next == '-':
			out.WriteByte(ch)
			out.WriteByte(next)
			i++
			inLineComment = true
		case ch == '/' && next == '*':
			out.WriteByte(ch)
			out.WriteByte(next)
			i++
			inBlockComment = true
		case ch == '?':
			out.WriteString("$")
			out.WriteString(strconv.Itoa(index))
			index++
		default:
			out.WriteByte(ch)
		}
	}
	return out.String()
}

func postgresDataSource(raw map[string]any) (string, []string, error) {
	pg, ok := postgresConnection(raw)
	if !ok {
		return "", nil, errors.New("postgres log database requires database.connection.postgres")
	}
	if dsn := cleanString(pg["dsn"]); dsn != "" {
		return dsn, postgresRedactions(dsn, ""), nil
	}
	host := cleanString(pg["host"])
	dbname := firstCleanString(pg["database"], pg["dbname"])
	if host == "" || dbname == "" {
		return "", nil, errors.New("postgres log database requires dsn or host and database/dbname")
	}
	port := cleanString(pg["port"])
	if port != "" {
		if _, err := strconv.Atoi(port); err != nil {
			return "", nil, fmt.Errorf("postgres log database port must be numeric")
		}
	}
	user := cleanString(pg["user"])
	password := cleanString(pg["password"])
	sslmode := cleanString(pg["sslmode"])
	parts := []string{
		postgresKeyword("host", host),
		postgresKeyword("dbname", dbname),
	}
	if port != "" {
		parts = append(parts, postgresKeyword("port", port))
	}
	if user != "" {
		parts = append(parts, postgresKeyword("user", user))
	}
	if password != "" {
		parts = append(parts, postgresKeyword("password", password))
	}
	if sslmode != "" {
		parts = append(parts, postgresKeyword("sslmode", sslmode))
	}
	return strings.Join(parts, " "), postgresRedactions("", password), nil
}

func applyDatabasePoolSettings(db *sql.DB, raw map[string]any) {
	if db == nil {
		return
	}
	poolSize := positiveConfigInt(raw["pool_size"])
	maxOverflow := positiveConfigInt(raw["max_overflow"])
	maxOpen := positiveConfigInt(raw["max_open_conns"])
	if maxOpen == 0 && poolSize > 0 {
		maxOpen = poolSize + maxOverflow
	}
	if maxOpen > 0 {
		db.SetMaxOpenConns(maxOpen)
	}
	maxIdle := positiveConfigInt(raw["max_idle_conns"])
	if maxIdle == 0 && poolSize > 0 {
		maxIdle = poolSize
	}
	if maxOpen > 0 && maxIdle > maxOpen {
		maxIdle = maxOpen
	}
	if maxIdle > 0 {
		db.SetMaxIdleConns(maxIdle)
	}
	if seconds := positiveConfigInt(raw["conn_max_lifetime_seconds"]); seconds > 0 {
		db.SetConnMaxLifetime(time.Duration(seconds) * time.Second)
	}
	if seconds := positiveConfigInt(raw["conn_max_idle_time_seconds"]); seconds > 0 {
		db.SetConnMaxIdleTime(time.Duration(seconds) * time.Second)
	}
}

func positiveConfigInt(value any) int {
	if text := cleanString(value); text != "" {
		if parsed, err := strconv.Atoi(text); err == nil && parsed > 0 {
			return parsed
		}
	}
	if out := intValue(value); out > 0 {
		return out
	}
	return 0
}

func postgresConnection(raw map[string]any) (map[string]any, bool) {
	connection, ok := toMap(raw["connection"])
	if !ok {
		return nil, false
	}
	if pg, ok := toMap(connection["postgres"]); ok {
		return pg, true
	}
	if pg, ok := toMap(connection["postgresql"]); ok {
		return pg, true
	}
	return nil, false
}

func firstCleanString(values ...any) string {
	for _, value := range values {
		if text := cleanString(value); text != "" {
			return text
		}
	}
	return ""
}

func postgresKeyword(key, value string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(value)
	return key + "='" + escaped + "'"
}

func redactDatabaseError(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	for _, secret := range secrets {
		if strings.TrimSpace(secret) != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	message = passwordAssignmentPattern.ReplaceAllString(message, "${1}[redacted]")
	message = postgresURLPasswordPattern.ReplaceAllString(message, "${1}[redacted]@")
	return errors.New(message)
}

func postgresRedactions(dsn string, password string) []string {
	out := make([]string, 0, 2)
	if password != "" {
		out = append(out, password)
	}
	if match := passwordAssignmentPattern.FindStringSubmatch(dsn); len(match) > 2 {
		for _, group := range match[2:] {
			if group != "" {
				out = append(out, strings.Trim(group, "'\""))
				break
			}
		}
	}
	if match := postgresURLPasswordPattern.FindStringSubmatch(dsn); len(match) > 2 {
		out = append(out, match[2])
	}
	return out
}

var (
	passwordAssignmentPattern  = regexp.MustCompile(`(?i)(password=)(?:'([^']*)'|"([^"]*)"|([^\s]+))`)
	postgresURLPasswordPattern = regexp.MustCompile(`(?i)(postgres(?:ql)?://[^:@\s]+:)([^@\s]+)@`)
)
