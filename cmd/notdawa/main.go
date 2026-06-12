// Command notdawa is the unified CLI for the local DAWA mirror: import registers
// from Datafordeler Fildownload, run migrations, provision missing data, and
// serve the API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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
	root.AddCommand(migrateCmd(), importCmd(), provisionCmd(), serveCmd())

	// SIGINT/SIGTERM cancel cmd.Context(): serve drains gracefully, and import
	// steps abort through their context so failRun can record the interruption
	// instead of leaving ingest_runs rows frozen at pending/downloaded.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := root.ExecuteContext(ctx); err != nil {
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
			// The serving pool runs with statement_timeout + bounded size;
			// the import/migrate lanes keep the untimed db.Connect.
			pool, err := db.ConnectServe(cmd.Context(), cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer pool.Close()

			handler, stopRecorder := api.NewHumaServer(pool, baseURL)
			srv := &http.Server{
				Addr:    addr,
				Handler: handler,
				// Slowloris guards. WriteTimeout stays generous: responses are
				// fully buffered before writing, but large bodies to slow
				// clients still need room.
				ReadHeaderTimeout: 10 * time.Second,
				ReadTimeout:       time.Minute,
				WriteTimeout:      15 * time.Minute,
				IdleTimeout:       2 * time.Minute,
				MaxHeaderBytes:    1 << 20,
			}

			errCh := make(chan error, 1)
			go func() { errCh <- srv.ListenAndServe() }()
			log.Printf("notdawa api listening on %s - OpenAPI at /openapi.json, docs at /docs", addr)

			select {
			case err := <-errCh:
				return err
			case <-cmd.Context().Done():
				// Rolling deploy / SIGTERM: drain in-flight requests, then
				// flush the traffic recorder's pending access_paths counts.
				// 25s fits inside k8s' default 30s termination grace period.
				log.Printf("shutting down: draining in-flight requests")
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
				defer cancel()
				err := srv.Shutdown(shutdownCtx)
				stopRecorder()
				if err != nil && !errors.Is(err, context.DeadlineExceeded) {
					return err
				}
				return nil
			}
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
