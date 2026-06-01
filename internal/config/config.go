// Package config loads notdawa configuration from the environment, with a
// best-effort read of a local .env file (no external dependency).
package config

import (
	"bufio"
	"os"
	"strings"
)

type Config struct {
	// DatafordelerAPIKey authenticates Fildownload + GraphQL calls.
	DatafordelerAPIKey string
	// DatabaseURL is the Postgres+PostGIS DSN (pgx format).
	DatabaseURL string
	// GSearchToken authenticates GSSearch (autocomplete) calls.
	GSearchToken string
}

// Load reads .env (if present) then the process environment.
func Load() Config {
	loadDotEnv(".env")
	return Config{
		DatafordelerAPIKey: os.Getenv("DATAFORDELER_API_KEY"),
		DatabaseURL:        envOr("DATABASE_URL", "postgres://notdawa:notdawa@localhost:5432/notdawa?sslmode=disable"),
		GSearchToken:       os.Getenv("GSEARCH_TOKEN"),
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// loadDotEnv sets any KEY=VALUE pairs from path that aren't already in the env.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"'`)
		if _, exists := os.LookupEnv(k); !exists {
			_ = os.Setenv(k, v)
		}
	}
}
