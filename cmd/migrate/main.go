package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	"github.com/Cumparator/smart_payment_method/internal/config"
	"github.com/Cumparator/smart_payment_method/migrations"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const usage = `
Использование:

go run ./cmd/migrate <command>

Доступные команды:

up          Применить все новые миграции
up-by-one   Применить одну миграцию
down        Откатить последнюю миграцию
status      Показать состояние миграций
version     Показать текущую версию БД
baseline    Зарегистрировать существующую старую схему`

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New(usage)
	}

	command := args[0]

	switch command {
	case "up", "up-by-one", "down", "status", "version", "baseline":
	default:
		return fmt.Errorf("unknown command: %s\n%s", command, usage)
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	db, err := sql.Open("pgx", cfg.Database)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return fmt.Errorf("ping database: %w", err)
	}

	if command == "baseline" {
		defer db.Close()

		if err := migrations.Baseline(ctx, db); err != nil {
			return err
		}

		fmt.Println("Baseline успешно выполнен. Текущая версия: 2")
		return nil
	}

	provider, err := migrations.NewProvider(db)
	if err != nil {
		db.Close()
		return err
	}

	defer provider.Close()

	switch command {
	case "up":
		results, err := provider.Up(ctx)
		if err != nil {
			return fmt.Errorf("apply migrations: %w", err)
		}

		if len(results) == 0 {
			fmt.Println("Новых миграций нет.")
			return nil
		}

		for _, result := range results {
			fmt.Println(result)
		}

	case "up-by-one":
		result, err := provider.UpByOne(ctx)
		if err != nil {
			return fmt.Errorf("apply one migration: %w", err)
		}

		fmt.Println(result)

	case "down":
		result, err := provider.Down(ctx)
		if err != nil {
			return fmt.Errorf("rollback migration: %w", err)
		}

		fmt.Println(result)

	case "status":
		current, target, err := provider.GetVersions(ctx)
		if err != nil {
			return fmt.Errorf("get migration versions: %w", err)
		}

		pending, err := provider.HasPending(ctx)
		if err != nil {
			return fmt.Errorf("check pending migrations: %w", err)
		}

		fmt.Printf("Текущая версия БД: %d\n", current)
		fmt.Printf("Последняя версия миграций: %d\n", target)
		fmt.Printf("Есть новые миграции: %t\n", pending)

	case "version":
		version, err := provider.GetDBVersion(ctx)
		if err != nil {
			return fmt.Errorf("get database version: %w", err)
		}

		fmt.Printf("Версия БД: %d\n", version)
	}

	return nil
}
