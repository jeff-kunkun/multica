-- DENE-2041: the Bot may ask for a key that lapses after N days
-- (`multica bot connect --expires-in-days N`). NULL or 0 keeps today's
-- behaviour: the key it collects never expires.
ALTER TABLE bot_connect_request ADD COLUMN key_expires_in_days integer
    CHECK (key_expires_in_days IS NULL OR key_expires_in_days >= 0);
