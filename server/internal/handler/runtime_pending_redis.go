package handler

import "github.com/redis/go-redis/v9"

// runtimePendingRedisHashTag pins every runtime pending-request key (model
// list, update) to one Redis Cluster slot so claimPendingScript can touch the
// pending zset and the record key in a single atomic script.
const runtimePendingRedisHashTag = "{runtime_pending}"

// claimPendingScript atomically claims a pending request:
//
//	KEYS[1] = pending zset    ARGV[1] = request id to claim
//	KEYS[2] = record key       ARGV[2] = new record JSON (status=running)
//	                           ARGV[3] = record TTL in seconds
//
// Returns 1 when this caller won the claim (zset entry removed, record
// updated), 0 when the entry was already gone (another node won).
// Either the ZREM and the SET both happen or neither does — Redis executes
// a Lua script as a single atomic unit.
var claimPendingScript = redis.NewScript(`
local removed = redis.call('ZREM', KEYS[1], ARGV[1])
if removed == 0 then
    return 0
end
redis.call('SET', KEYS[2], ARGV[2], 'EX', tonumber(ARGV[3]))
return 1
`)
