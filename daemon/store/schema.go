package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"strings"
)

//go:embed sql/*.sql
var schemaFiles embed.FS

const schemaVersion = 1

func installSchema(ctx context.Context, db *sql.DB, name string) error {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > schemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d", version, schemaVersion)
	}
	if version == schemaVersion {
		return nil
	}
	contents, err := schemaFiles.ReadFile(name)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range strings.Split(string(contents), ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply %s statement: %w", name, err)
		}
	}
	return tx.Commit()
}
