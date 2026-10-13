-- The baseline cannot be rolled back: it is the whole schema. Restore a backup
-- or drop the database instead.
DO $$
BEGIN
  RAISE EXCEPTION 'migration 001_baseline cannot be rolled back; restore a backup or recreate the database';
END
$$;
