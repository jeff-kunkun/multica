package migrations

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestChannelTaskDeliveryBackfillSnapshotsOnlyTasksWithLiveRoutingData(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("integration test requires Postgres at DATABASE_URL")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect to Postgres: %v", err)
	}
	defer pool.Close()

	for _, tc := range []struct {
		name        string
		withBinding bool
		wantCount   int
	}{
		{name: "backfills resolvable task", withBinding: true, wantCount: 1},
		{name: "skips task whose installation route was removed", wantCount: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := "channel_delivery_backfill_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			quotedSchema := pgx.Identifier{schema}.Sanitize()
			if _, err := pool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
				t.Fatalf("create schema: %v", err)
			}
			t.Cleanup(func() {
				_, _ = pool.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE")
			})

			conn, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatalf("acquire connection: %v", err)
			}
			defer conn.Release()
			if _, err := conn.Exec(ctx, `SELECT set_config('search_path', $1, false)`, schema); err != nil {
				t.Fatalf("set search path: %v", err)
			}
			if _, err := conn.Exec(ctx, `
				CREATE TABLE agent_task_queue (
					id UUID PRIMARY KEY,
					chat_session_id UUID,
					chat_input_task_id UUID,
					status TEXT NOT NULL
				);
				CREATE TABLE chat_message (
					task_id UUID,
					role TEXT NOT NULL,
					channel_ingested BOOLEAN NOT NULL
				);
				CREATE TABLE channel_chat_session_binding (
					id UUID PRIMARY KEY,
					installation_id UUID NOT NULL,
					channel_type TEXT NOT NULL,
					channel_chat_id TEXT NOT NULL,
					chat_type TEXT NOT NULL,
					last_message_id TEXT,
					last_thread_id TEXT,
					route_revision BIGINT NOT NULL,
					config JSONB NOT NULL,
					chat_session_id UUID NOT NULL
				);
				CREATE TABLE channel_task_delivery (
					task_id UUID PRIMARY KEY,
					binding_id UUID NOT NULL,
					installation_id UUID NOT NULL,
					channel_type TEXT NOT NULL,
					channel_chat_id TEXT NOT NULL,
					chat_type TEXT NOT NULL,
					channel_message_id TEXT,
					channel_thread_id TEXT,
					route_revision BIGINT NOT NULL,
					config JSONB NOT NULL
				);
			`); err != nil {
				t.Fatalf("create fixture tables: %v", err)
			}

			const taskID = "11111111-1111-1111-1111-111111111111"
			const sessionID = "22222222-2222-2222-2222-222222222222"
			if _, err := conn.Exec(ctx, `
				INSERT INTO agent_task_queue (id, chat_session_id, status)
				VALUES ($1, $2, 'running')
			`, taskID, sessionID); err != nil {
				t.Fatalf("seed task: %v", err)
			}
			if _, err := conn.Exec(ctx, `
				INSERT INTO chat_message (task_id, role, channel_ingested)
				VALUES ($1, 'user', TRUE)
			`, taskID); err != nil {
				t.Fatalf("seed message: %v", err)
			}
			if tc.withBinding {
				if _, err := conn.Exec(ctx, `
					INSERT INTO channel_chat_session_binding (
						id, installation_id, channel_type, channel_chat_id, chat_type,
						last_message_id, last_thread_id, route_revision, config, chat_session_id
					) VALUES (
						'33333333-3333-3333-3333-333333333333',
						'44444444-4444-4444-4444-444444444444',
						'slack', 'D1', 'p2p', 'm1', 'm1', 1, '{}', $1
					)
				`, sessionID); err != nil {
					t.Fatalf("seed binding: %v", err)
				}
			}

			_, err = conn.Exec(ctx, readMigrationFile(t, "427_channel_task_delivery_backfill.up.sql"))
			if err != nil {
				t.Fatalf("apply migration: %v", err)
			}
			var count int
			if err := conn.QueryRow(ctx, `SELECT count(*) FROM channel_task_delivery WHERE task_id = $1`, taskID).Scan(&count); err != nil {
				t.Fatalf("count deliveries: %v", err)
			}
			if count != tc.wantCount {
				t.Fatalf("delivery rows = %d, want %d", count, tc.wantCount)
			}
		})
	}
}

func TestChannelTaskDeliveryPrimaryKeyReusesConcurrentIndex(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("integration test requires Postgres at DATABASE_URL")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect to Postgres: %v", err)
	}
	defer pool.Close()

	schema := "channel_delivery_pkey_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE")
	})

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT set_config('search_path', $1, false)`, schema); err != nil {
		t.Fatalf("set search path: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		CREATE TABLE channel_task_delivery (
			task_id UUID NOT NULL,
			binding_id UUID NOT NULL
		)
	`); err != nil {
		t.Fatalf("create delivery table: %v", err)
	}
	if _, err := conn.Exec(ctx, readMigrationFile(t, "423_channel_task_delivery_pkey_index.up.sql")); err != nil {
		t.Fatalf("build concurrent unique index: %v", err)
	}
	if _, err := conn.Exec(ctx, readMigrationFile(t, "424_channel_task_delivery_primary_key.up.sql")); err != nil {
		t.Fatalf("attach primary key: %v", err)
	}

	var constraintType string
	var primary, valid bool
	if err := conn.QueryRow(ctx, `
		SELECT constraint_row.contype::text, index_row.indisprimary, index_row.indisvalid
		FROM pg_constraint AS constraint_row
		JOIN pg_index AS index_row ON index_row.indexrelid = constraint_row.conindid
		WHERE constraint_row.conrelid = to_regclass($1)
		  AND constraint_row.conname = 'channel_task_delivery_pkey'
	`, schema+".channel_task_delivery").Scan(&constraintType, &primary, &valid); err != nil {
		t.Fatalf("inspect primary key: %v", err)
	}
	if constraintType != "p" || !primary || !valid {
		t.Fatalf("constraint type=%q primary=%t valid=%t", constraintType, primary, valid)
	}
}
