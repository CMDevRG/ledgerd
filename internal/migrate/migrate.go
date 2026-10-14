// Package migrate applies the embedded schema migrations with goose.
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/pressly/goose/v3"

	// Registers the "pgx" database/sql driver.
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/CMDevRG/ledgerd/migrations"
)

func provider(db *sql.DB) (*goose.Provider, error) {
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		return nil, fmt.Errorf("create migration provider: %w", err)
	}
	return p, nil
}

func open(dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	return db, nil
}

// Up applies all pending migrations. It must be called with the owner role.
func Up(ctx context.Context, dsn string, log *slog.Logger) error {
	db, err := open(dsn)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // closing a read-mostly handle at exit

	p, err := provider(db)
	if err != nil {
		return err
	}
	results, err := p.Up(ctx)
	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	for _, r := range results {
		log.Info("migration applied", "version", r.Source.Version, "file", r.Source.Path, "duration", r.Duration)
	}
	if len(results) == 0 {
		log.Info("no pending migrations")
	}
	return nil
}

// DownAll rolls every migration back. Intended for tests and local development.
func DownAll(ctx context.Context, dsn string) error {
	db, err := open(dsn)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck // closing a read-mostly handle at exit

	p, err := provider(db)
	if err != nil {
		return err
	}
	if _, err := p.DownTo(ctx, 0); err != nil {
		return fmt.Errorf("roll back migrations: %w", err)
	}
	return nil
}
