// Cleanup defaults to a read-only inventory; --apply reserves and resumes deletion.
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
	"github.com/rickyroynardson/watermarker/apps/api/internal/storage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	days := flag.Int("retention-days", 30, "retain terminal batches for at least this many days")
	apply := flag.Bool("apply", false, "expire eligible batches, schedule deletion and resume due deletes")
	limit := flag.Int("limit", 100, "maximum batches to expire, keys to schedule and deletes to attempt (1-1000)")
	flag.Parse()
	if *limit < 1 || *limit > 1000 {
		return fmt.Errorf("limit must be 1-1000")
	}
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
	if *apply {
		objects, err := storage.NewS3(ctx, os.Getenv("S3_BUCKET"))
		if err != nil {
			return err
		}
		if err := objects.RequireUnversioned(ctx); err != nil {
			return err
		}
		scheduled, err := cleanup.Reserve(ctx, db, before, *limit)
		if err != nil {
			return err
		}
		deleted, failed := 0, 0
		for i := 0; i < *limit && ctx.Err() == nil; i++ {
			attempted, err := cleanup.DeleteOne(ctx, db, objects.Delete)
			if err != nil {
				failed++
				fmt.Fprintln(os.Stderr, "cleanup deletion:", err)
				if !attempted {
					break
				}
			} else if attempted {
				deleted++
			}
			if !attempted {
				break
			}
		}
		if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"dry_run": false, "scheduled": scheduled, "deleted": deleted, "failed": failed}); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if failed > 0 {
			return fmt.Errorf("%d cleanup deletions failed; progress saved for retry", failed)
		}
		return nil
	}
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
