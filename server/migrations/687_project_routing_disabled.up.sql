-- routing_disabled turns automatic dispatch off for one project (DENE-1966).
-- Its tickets are left exactly as a workspace with routing off leaves them:
-- routing does not pick, replace or drop an executor; manual assignment and
-- an agent's own pick stand. Default false keeps every project as before.
ALTER TABLE project
    ADD COLUMN IF NOT EXISTS routing_disabled BOOLEAN NOT NULL DEFAULT false;
