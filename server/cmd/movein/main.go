// movein moves a workspace of the old (kun-line) Multica server into a
// workspace here, once, to start over: projects arrive as they were, every
// issue arrives done (or cancelled) with its comments, and every chat arrives
// read. Nothing is assigned, routed, notified or run. Running it again skips
// what already arrived.
//
// It reads the old server through its API with a personal access token and
// writes this server's database directly. Run it before the mirror is turned
// on (see docs/kun-main/instance.md); afterwards this whole directory can go.
//
//	MULTICA_MOVEIN_TOKEN=mul_... DATABASE_URL=postgres://... \
//	  go run ./cmd/movein --from https://ai.example.com --from-workspace kunkunya --to-workspace <slug>
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	from := flag.String("from", "https://ai.ferryway.cc", "old server URL; use the same value as MULTICA_MIRROR_TARGET_URL")
	fromWorkspace := flag.String("from-workspace", "kunkunya", "old workspace slug or id")
	toWorkspace := flag.String("to-workspace", "", "local workspace slug or id")
	as := flag.String("as", "", "email of the local member everything is written as (default: the workspace owner)")
	flag.Parse()

	token := os.Getenv("MULTICA_MOVEIN_TOKEN")
	dbURL := os.Getenv("DATABASE_URL")
	if token == "" || dbURL == "" || *toWorkspace == "" {
		fmt.Fprintln(os.Stderr, "movein needs MULTICA_MOVEIN_TOKEN, DATABASE_URL and --to-workspace")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Error("connect database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	m := &mover{old: newOldServer(*from, token), pool: pool, log: log}
	if err := m.prepare(ctx, *from, *fromWorkspace, *toWorkspace, *as); err != nil {
		log.Error("prepare", "error", err)
		os.Exit(1)
	}
	err = m.run(ctx)
	out, _ := json.MarshalIndent(m.stats, "", "  ")
	fmt.Println(string(out))
	if err != nil {
		log.Error("move stopped; run again to continue", "error", err)
		os.Exit(1)
	}
}
