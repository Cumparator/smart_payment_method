// Package config — конфигурация сервиса через переменные окружения.
package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config — настройки сервиса.
type Config struct {
	Addr     string
	Database string
	LogLevel string
}

// Load читает конфигурацию из переменных окружения.
func Load() (Config, error) {
	c := Config{
		Addr:     getenv("SPM_ADDR", ":8080"),
		Database: getenv("SPM_DATABASE_URL", "postgres://spm:spm@localhost:5432/spm?sslmode=disable"),
		LogLevel: getenv("SPM_LOG_LEVEL", "info"),
	}
	if c.Addr == "" {
		return c, fmt.Errorf("SPM_ADDR не может быть пустым")
	}
	if c.Database == "" {
		return c, fmt.Errorf("SPM_DATABASE_URL не может быть пустым")
	}
	return c, nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func atoi(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
