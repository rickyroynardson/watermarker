// Cleanup prints a read-only retention inventory. It never deletes data.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/rickyroynardson/watermarker/apps/api/internal/cleanup"
	"github.com/rickyroynardson/watermarker/apps/api/internal/database"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	days := flag.Int("retention-days", 30, "retain terminal batches for at least this many days")
	flag.Parse()
	if *days < 1 || *days > 36500 || flag.NArg() != 0 {
		return fmt.Errorf("retention-days must be between 1 and 36500; no positional arguments supported")
	}
	_ = godotenv.Load()
	if os.Getenv("DATABASE_URL") == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	db, err := database.ConnectPgx(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer db.Close()
	now := time.Now().UTC()
	before := now.Add(-time.Duration(*days) * 24 * time.Hour)
	candidates, err := cleanup.Plan(ctx, db, before)
	if err != nil {
		return err
	}
	report := struct {
		DryRun          bool                `json:"dry_run"`
		GeneratedAt     time.Time           `json:"generated_at"`
		CompletedBefore time.Time           `json:"completed_before"`
		RetentionDays   int                 `json:"retention_days"`
		CandidateCount  int                 `json:"candidate_count"`
		Candidates      []cleanup.Candidate `json:"candidates"`
	}{true, now, before, *days, len(candidates), candidates}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}
