package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
)

// OpenReadOnly never creates a database, applies migrations or updates agents.
func OpenReadOnly(path string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("mode", "ro")
	q.Set("_pragma", "query_only(ON)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	if err = db.PingContext(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("open existing ledger read-only: %w", err)
	}
	return db, nil
}
