package handler

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	"github.com/redis/go-redis/v9"
)

const redisTestDB = 14

var redisACLTestUserSequence atomic.Uint64

// newRedisTestClient connects to the Redis instance indicated by REDIS_TEST_URL
// and flushes this package's logical test DB so each test starts from a clean
// slate. The helper skips the calling test if the env var is unset — matches
// the DATABASE_URL gating in the rest of the suite so `go test ./...` still
// works on a stock laptop without a running Redis.
func newRedisTestClient(t *testing.T) *redis.Client {
	t.Helper()
	url := os.Getenv("REDIS_TEST_URL")
	if url == "" {
		t.Skip("REDIS_TEST_URL not set")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatalf("parse REDIS_TEST_URL: %v", err)
	}
	opts.DB = redisTestDB
	rdb := redis.NewClient(opts)
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("REDIS_TEST_URL unreachable: %v", err)
	}
	if err := rdb.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flushdb: %v", err)
	}
	t.Cleanup(func() {
		rdb.FlushDB(context.Background())
		rdb.Close()
	})
	return rdb
}

func newRedisTestClientWithoutMulti(t *testing.T) *redis.Client {
	t.Helper()
	admin := newRedisTestClient(t)
	ctx := context.Background()
	username := fmt.Sprintf("runtime-no-multi-%d", redisACLTestUserSequence.Add(1))
	const password = "runtime-no-multi-test-password"

	if err := admin.Do(
		ctx,
		"ACL", "SETUSER", username,
		"reset", "on", ">"+password, "~*", "+@all", "-multi", "-exec",
	).Err(); err != nil {
		t.Fatalf("create restricted Redis user: %v", err)
	}
	t.Cleanup(func() {
		if err := admin.Do(context.Background(), "ACL", "DELUSER", username).Err(); err != nil {
			t.Errorf("delete restricted Redis user: %v", err)
		}
	})

	opts := *admin.Options()
	opts.Username = username
	opts.Password = password
	restricted := redis.NewClient(&opts)
	t.Cleanup(func() { _ = restricted.Close() })
	if err := restricted.Ping(ctx).Err(); err != nil {
		t.Fatalf("connect as restricted Redis user: %v", err)
	}
	return restricted
}
