package migrations

import (
	"database/sql"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"
)

// Встраиваем SQL-миграции в Go-бинарник.
//
//go:embed *.sql
var migrationFS embed.FS

// NewProvider создаёт Goose Provider для PostgreSQL.
func NewProvider(db *sql.DB) (*goose.Provider, error) {
	if db == nil {
		return nil, fmt.Errorf("migrations: database connection is nil")
	}

	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		migrationFS,
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		return nil, fmt.Errorf("migrations: create goose provider: %w", err)
	}

	return provider, nil
}
