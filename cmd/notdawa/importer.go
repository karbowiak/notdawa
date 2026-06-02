package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/karbowiak/notdawa/internal/datafordeler"
	"github.com/karbowiak/notdawa/internal/db"
	"github.com/karbowiak/notdawa/internal/ingest"
)

// importStep is one atomic entity in the full-import pipeline: download +
// transform + load one register entity into Postgres. run adapts every ingest
// function to a single signature (some ignore the client, e.g. the derived
// post-processing steps).
type importStep struct {
	key      string // CLI selector, e.g. "regioner"
	register string // DAR | DAGI | MAT | DS | derived
	desc     string // short human description for the plan table
	run      func(context.Context, *pgxpool.Pool, *datafordeler.Client) (ingest.Result, error)
}

// importPlan is the canonical full-import order. Steps run top-to-bottom and the
// order encodes the real dependencies: the address core (navngivenvej → access
// addresses → unit addresses) precedes postnumre-kommuner, which derives the
// postal↔municipality relation from dar_husnummer; polylabel-backfill runs last,
// over all loaded geometry. Atomic ingest functions are wrapped so they share
// one signature even when they take no Fildownload client.
func importPlan() []importStep {
	di := func(f func(context.Context, *pgxpool.Pool, *datafordeler.Client) (ingest.Result, error)) func(context.Context, *pgxpool.Pool, *datafordeler.Client) (ingest.Result, error) {
		return f
	}
	// noClient adapts an ingest function that needs only ctx+pool.
	noClient := func(f func(context.Context, *pgxpool.Pool) (ingest.Result, error)) func(context.Context, *pgxpool.Pool, *datafordeler.Client) (ingest.Result, error) {
		return func(ctx context.Context, p *pgxpool.Pool, _ *datafordeler.Client) (ingest.Result, error) {
			return f(ctx, p)
		}
	}
	return []importStep{
		// DAGI — administrative geographies.
		{"regioner", "DAGI", "administrative regions", di(ingest.Regioner)},
		{"kommuner", "DAGI", "municipalities", di(ingest.Kommuner)},
		{"landsdele", "DAGI", "NUTS3 parts of the country", di(ingest.Landsdele)},
		{"sogne", "DAGI", "parishes", di(ingest.Sogne)},
		{"postnumre", "DAGI", "postal districts", di(ingest.Postnumre)},
		{"supplerendebynavne", "DAGI", "supplementary town names", di(ingest.Supplerendebynavne)},
		{"opstillingskredse", "DAGI", "nomination districts (geometry)", di(ingest.Opstillingskredse)},
		{"storkredse-valglandsdele", "DAGI", "constituencies + electoral regions", di(ingest.StorkredseValglandsdele)},
		{"afstemningsomraader", "DAGI", "polling districts", di(ingest.Afstemningsomraader)},
		{"opstillingskredse-finish", "derived", "nomination-district centres + kommune links", noClient(ingest.OpstillingskredseFinish)},
		{"menighedsraadsafstemningsomraader", "DAGI", "parish-council polling districts", di(ingest.MRAfstemningsomraader)},
		{"retskredse", "DAGI", "judicial districts", di(ingest.Retskredse)},
		{"politikredse", "DAGI", "police districts", di(ingest.Politikredse)},
		{"zoner", "DAGI", "planning zones", di(ingest.Zoner)},

		// MAT — cadastre.
		{"ejerlav", "MAT", "cadastral districts", di(ingest.Ejerlav)},
		{"mat-sfe", "MAT", "samlet fast ejendom (properties)", di(ingest.SamletFastEjendom)},
		{"mat-jordstykke", "MAT", "land parcels", di(ingest.Jordstykke)},
		{"mat-lodflade", "MAT", "parcel faces → parcel geometry", di(ingest.Lodflade)},

		// DAR — addresses (dependency order: roads → access → unit addresses).
		{"navngivenvej", "DAR", "named roads", di(ingest.NavngivenVej)},
		{"nvkommunedel", "DAR", "road↔municipality links", di(ingest.NavngivenVejKommunedel)},
		{"nvpostnummer", "DAR", "road↔postal-district links", di(ingest.NavngivenVejPostnummer)},
		{"darpostnummer", "DAR", "DAR postal districts", di(ingest.DARPostnummer)},
		{"adressepunkt", "DAR", "address points (geometry)", di(ingest.Adressepunkt)},
		{"husnummer", "DAR", "access addresses", di(ingest.Husnummer)},
		{"adresse", "DAR", "unit addresses", di(ingest.Adresse)},

		// DS — place names.
		{"ds", "DS", "place names (stednavne)", di(ingest.DS)},

		// Derived / post-processing — run after the data they read from is loaded.
		{"postnumre-kommuner", "derived", "postal↔municipality relation", noClient(ingest.PostnumreKommuner)},
		{"stormodtagere", "derived", "high-volume postal recipients (seed CSV)", noClient(ingest.Stormodtagere)},
		{"brofasthed", "DAWA-seed", "per-place land-connectedness flag (seed CSV)", noClient(ingest.Brofasthed)},
		{"polylabel-backfill", "derived", "label points for all geometry", noClient(ingest.PolylabelBackfill)},
	}
}

// importGroups are convenience selectors that expand to a contiguous set of plan
// steps (e.g. "import election"). "all" (the default) runs the whole plan.
var importGroups = map[string][]string{
	"election":      {"opstillingskredse", "storkredse-valglandsdele", "afstemningsomraader", "opstillingskredse-finish"},
	"vejlinks":      {"nvkommunedel", "nvpostnummer", "darpostnummer"},
	"adresser-core": {"adressepunkt", "husnummer", "adresse"},
	"mat":           {"mat-sfe", "mat-jordstykke", "mat-lodflade"},
}

// selectSteps resolves a CLI target into the ordered steps to run. Empty or
// "all" → the whole plan; a group name → its steps (in plan order); otherwise an
// exact atomic key. Unknown targets return a helpful error listing the choices.
func selectSteps(target string) ([]importStep, error) {
	plan := importPlan()
	byKey := make(map[string]importStep, len(plan))
	for _, s := range plan {
		byKey[s.key] = s
	}

	switch target {
	case "", "all":
		return plan, nil
	}
	if keys, ok := importGroups[target]; ok {
		out := make([]importStep, 0, len(keys))
		for _, k := range keys {
			out = append(out, byKey[k])
		}
		return out, nil
	}
	if s, ok := byKey[target]; ok {
		return []importStep{s}, nil
	}

	keys := make([]string, 0, len(plan))
	for _, s := range plan {
		keys = append(keys, s.key)
	}
	groups := make([]string, 0, len(importGroups))
	for g := range importGroups {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	return nil, fmt.Errorf("unknown import target %q\n\ngroups:  all, %s\nentities: %s",
		target, strings.Join(groups, ", "), strings.Join(keys, ", "))
}

func importCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "import [target]",
		Short: "Download + transform + load registers into Postgres (default: everything)",
		Long: "Import register extracts from Datafordeler Fildownload into the local mirror.\n\n" +
			"With no target it runs the full pipeline in dependency order. A target may be a\n" +
			"group (all, election, vejlinks, adresser-core, mat) or a single entity (e.g. regioner).",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			steps, err := selectSteps(regArg(args, "all"))
			if err != nil {
				return err
			}

			// Always show what would be pulled.
			printImportPlan(steps)

			if dryRun {
				return nil
			}
			if err := requireKey(); err != nil {
				return err
			}

			pool, err := db.Connect(cmd.Context(), cfg.DatabaseURL)
			if err != nil {
				return err
			}
			defer pool.Close()
			client := datafordeler.New(cfg.DatafordelerAPIKey)

			return runImport(cmd.Context(), pool, client, steps)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the import plan and exit without downloading")
	return cmd
}

// printImportPlan renders the table of everything the run will pull.
func printImportPlan(steps []importStep) {
	fmt.Printf("Import plan — %d step(s):\n\n", len(steps))
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "  #\tREGISTER\tENTITY\tDESCRIPTION")
	for i, s := range steps {
		fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\n", i+1, s.register, s.key, s.desc)
	}
	tw.Flush()
	fmt.Println()
}

// runImport executes each step in order, rendering a live progress line per
// step. A single step failure aborts the run (later steps may depend on it).
func runImport(ctx context.Context, pool *pgxpool.Pool, client *datafordeler.Client, steps []importStep) error {
	keyCol := 0
	for _, s := range steps {
		if n := len(s.key); n > keyCol {
			keyCol = n
		}
	}
	if keyCol > 22 {
		keyCol = 22
	}

	r := newRenderer(isTTY(os.Stdout), keyCol)
	defer r.close()
	client.OnProgress = func(dl, tot int64) { r.onDownload(dl, tot) }
	defer func() { client.OnProgress = nil }()

	for i, s := range steps {
		r.begin(i+1, len(steps), s.register, s.key)
		res, err := s.run(ctx, pool, client)
		r.finish(res, err)
		if err != nil {
			return fmt.Errorf("import %s: %w", s.key, err)
		}
	}
	fmt.Println("\nImport complete.")
	return nil
}

// --- progress renderer ---------------------------------------------------

const spinFrames = `⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏`

// renderer draws one updating progress line per step. On a TTY a background
// ticker animates a spinner and repaints in place; off a TTY (pipe/CI) it falls
// back to plain start/finish lines with no carriage returns.
type renderer struct {
	tty    bool
	keyCol int

	mu   sync.Mutex
	cur  *stepState
	spin []rune
	tick int
	done chan struct{}
}

type stepState struct {
	idx, total int
	register   string
	key        string
	phase      string // starting | downloading | loading
	dl, dlTot  int64
	start      time.Time
}

func newRenderer(tty bool, keyCol int) *renderer {
	r := &renderer{tty: tty, keyCol: keyCol, spin: []rune(spinFrames), done: make(chan struct{})}
	if tty {
		go r.animate()
	}
	return r
}

func (r *renderer) animate() {
	t := time.NewTicker(120 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-r.done:
			return
		case <-t.C:
			r.mu.Lock()
			if r.cur != nil {
				r.tick++
				r.draw()
			}
			r.mu.Unlock()
		}
	}
}

func (r *renderer) close() {
	if r.tty {
		close(r.done)
	}
}

func (r *renderer) begin(idx, total int, register, key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cur = &stepState{idx: idx, total: total, register: register, key: key, phase: "starting", start: time.Now()}
	if r.tty {
		r.draw()
	} else {
		fmt.Printf("→ [%d/%d] %s %s …\n", idx, total, register, key)
	}
}

func (r *renderer) onDownload(dl, tot int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur == nil {
		return
	}
	r.cur.dl, r.cur.dlTot = dl, tot
	if tot > 0 && dl >= tot {
		r.cur.phase = "loading"
	} else {
		r.cur.phase = "downloading"
	}
	if r.tty {
		r.draw()
	}
}

func (r *renderer) finish(res ingest.Result, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur == nil {
		return
	}
	c := r.cur
	r.cur = nil
	dur := time.Since(c.start).Round(100 * time.Millisecond)
	prefix := fmt.Sprintf("[%d/%d] %s %s", c.idx, c.total, c.register, padRight(c.key, r.keyCol))
	if r.tty {
		fmt.Print("\r\x1b[K") // clear the in-place line before the final one
	}
	if err != nil {
		fmt.Printf("%s  %s FAILED: %v\n", prefix, mark("✗", colRed, r.tty), err)
		return
	}
	meta := fmt.Sprintf("%d rows", res.RowsLoaded)
	if res.GenerationNumber > 0 {
		meta += fmt.Sprintf(" · gen %d", res.GenerationNumber)
	}
	fmt.Printf("%s  %s %s · %s\n", prefix, mark("✓", colGreen, r.tty), meta, dur)
}

// draw repaints the active step's progress line in place. Caller holds r.mu and
// r.tty is true.
func (r *renderer) draw() {
	c := r.cur
	sp := string(r.spin[r.tick%len(r.spin)])
	prefix := fmt.Sprintf("[%d/%d] %s %s", c.idx, c.total, c.register, padRight(c.key, r.keyCol))
	var status string
	switch c.phase {
	case "downloading":
		status = fmt.Sprintf("%s downloading %s %s", sp, bar(c.dl, c.dlTot, 16), bytesProgress(c.dl, c.dlTot))
	case "loading":
		status = fmt.Sprintf("%s loading rows…", sp)
	default:
		status = fmt.Sprintf("%s starting…", sp)
	}
	fmt.Printf("\r\x1b[K%s  %s", prefix, status)
}

// --- small formatting helpers --------------------------------------------

const (
	colGreen = "\x1b[32m"
	colRed   = "\x1b[31m"
	colReset = "\x1b[0m"
)

func mark(s, color string, tty bool) string {
	if !tty {
		return s
	}
	return color + s + colReset
}

func padRight(s string, n int) string {
	if len([]rune(s)) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len([]rune(s)))
}

// bar renders a fixed-width [████░░░░] meter. With no known total it shows an
// empty frame (the spinner conveys liveness).
func bar(done, total int64, width int) string {
	filled := 0
	if total > 0 {
		filled = int(int64(width) * done / total)
		if filled > width {
			filled = width
		}
	}
	return "▕" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "▏"
}

func bytesProgress(done, total int64) string {
	if total > 0 {
		return fmt.Sprintf("%s/%s", humanBytes(done), humanBytes(total))
	}
	return humanBytes(done)
}

// isTTY reports whether f is a character device (an interactive terminal) so we
// only emit carriage-return repaints and ANSI when a human is watching.
func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
