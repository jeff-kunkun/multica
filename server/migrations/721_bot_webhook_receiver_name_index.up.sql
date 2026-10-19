CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS bot_webhook_receiver_bot_name ON bot_webhook_receiver (bot_id, name);
