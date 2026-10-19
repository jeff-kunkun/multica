-- Attach the CONCURRENTLY-built unique index as the table's primary key.
ALTER TABLE plan
    ADD CONSTRAINT plan_pkey PRIMARY KEY USING INDEX plan_pkey_uidx;
