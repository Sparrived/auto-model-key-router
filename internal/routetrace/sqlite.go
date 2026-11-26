package routetrace

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const dbFilename = "route-traces.sqlite3"

const schema = `
CREATE TABLE IF NOT EXISTS route_traces (
  request_id TEXT PRIMARY KEY NOT NULL,
  owner_type TEXT NOT NULL,
  owner_tag BLOB NOT NULL,
  started_at INTEGER NOT NULL,
  finished_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  document_json BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS route_traces_expires ON route_traces(expires_at);
CREATE INDEX IF NOT EXISTS route_traces_finished ON route_traces(finished_at, request_id);
`

func openSQLite(ctx context.Context, path string, create bool) (*sql.DB, map[string]struct{}, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	path = filepath.ToSlash(absolute)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	mode := "rw"
	if create {
		mode = "rwc"
	}
	uri := url.URL{Scheme: "file", Path: path, RawQuery: "mode=" + mode + "&_pragma=busy_timeout(400)&_pragma=synchronous(NORMAL)"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, nil, err
	}
	db.SetMaxOpenConns(4)
	ids, err := initializeSQLite(ctx, db)
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	return db, ids, nil
}

func initializeSQLite(ctx context.Context, db *sql.DB) (map[string]struct{}, error) {
	var journal string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&journal); err != nil {
		return nil, err
	}
	if journal != "wal" {
		return nil, fmt.Errorf("SQLite WAL 不可用")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT request_id FROM route_traces LIMIT ?", maxRows+1)
	if err != nil {
		return nil, err
	}
	ids, err := scanIDs(rows, maxRows)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

func scanIDs(rows *sql.Rows, limit int) (map[string]struct{}, error) {
	defer rows.Close()
	ids := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if !ValidID(id) || len(ids) == limit {
			return nil, fmt.Errorf("追踪索引不符合约束")
		}
		ids[id] = struct{}{}
	}
	return ids, rows.Err()
}

func insertRecord(ctx context.Context, db *sql.DB, c *Collector, doc document, body []byte, overflow int) (map[string]struct{}, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO route_traces
		(request_id, owner_type, owner_tag, started_at, finished_at, expires_at, document_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, c.ID(), c.owner.kind, c.owner.tag[:], doc.StartedAt.UnixNano(),
		doc.FinishedAt.UnixNano(), doc.FinishedAt.Add(c.retention).UnixNano(), body)
	if err != nil {
		return nil, err
	}
	var removed map[string]struct{}
	if overflow > 0 {
		rows, err := tx.QueryContext(ctx, `DELETE FROM route_traces WHERE request_id IN
			(SELECT request_id FROM route_traces ORDER BY finished_at, request_id LIMIT ?) RETURNING request_id`, overflow)
		if err != nil {
			return nil, err
		}
		removed, err = scanIDs(rows, overflow)
		if err != nil {
			return nil, err
		}
	}
	return removed, tx.Commit()
}

func deleteExpired(ctx context.Context, db *sql.DB, now time.Time, batch int) (map[string]struct{}, error) {
	rows, err := db.QueryContext(ctx, `DELETE FROM route_traces WHERE request_id IN
		(SELECT request_id FROM route_traces WHERE expires_at <= ? ORDER BY expires_at LIMIT ?) RETURNING request_id`,
		now.UnixNano(), batch)
	if err != nil {
		return nil, err
	}
	return scanIDs(rows, batch)
}
