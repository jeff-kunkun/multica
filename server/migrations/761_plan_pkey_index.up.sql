-- Backing index for plan's primary key, attached in 762.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS plan_pkey_uidx
    ON plan (id);
