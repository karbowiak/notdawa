// Command notdawa is the unified CLI for the local DAWA mirror: import registers
// from Datafordeler Fildownload, run migrations, and serve the API.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/spf13/cobra"

	"github.com/karbowiak/notdawa/internal/api"
	"github.com/karbowiak/notdawa/internal/config"
	"github.com/karbowiak/notdawa/internal/dawa"
	"github.com/karbowiak/notdawa/internal/db"
)

var cfg config.Config

func main() {
	cobra.OnInitialize(func() { cfg = config.Load() })

	root := &cobra.Command{
		Use:           "notdawa",
		Short:         "Self-hosted, DAWA-compatible API backed by a local Datafordeler mirror",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(migrateCmd(), importCmd(), serveCmd())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func regArg(args []string, def string) string {
	if len(args) > 0 {
		return args[0]
	}
	return def
}

func requireKey() error {
	if cfg.DatafordelerAPIKey == "" {
		return fmt.Errorf("DATAFORDELER_API_KEY is not set — add it to .env (see .env.example) or export it in the environment")
	}
	return nil
}

func migrateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Apply SQL migrations to Postgres+PostGIS",
		RunE: func(cmd *cobra.Command, args []string) error {
			pool, err := db.Connect(cmd.Context(), cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer pool.Close()
			applied, err := db.Migrate(cmd.Context(), pool, "migrations")
			for _, a := range applied {
				fmt.Println("applied", a)
			}
			if err != nil {
				return err
			}
			fmt.Printf("migrations done (%d applied).\n", len(applied))
			return nil
		},
	}
}

func serveCmd() *cobra.Command {
	var addr, baseURL string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the DAWA-compatible HTTP API",
		RunE: func(cmd *cobra.Command, args []string) error {
			pool, err := db.Connect(cmd.Context(), cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer pool.Close()

			handler := api.NewHumaServer(pool, baseURL)
			log.Printf("notdawa api listening on %s - OpenAPI at /openapi.json, docs at /docs", addr)
			return http.ListenAndServe(addr, handler)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", ":8080", "address to listen on")
	cmd.Flags().StringVar(&baseURL, "base-url", dawa.DefaultBaseURL, "href base URL")
	return cmd
}

func humanBytes(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(u), 0
	for x := n / u; x >= u; x /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
