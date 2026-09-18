-- Rolling back drops every in-flight alignment conversation's structured state.
-- Issues already created from a draft are untouched: they are ordinary issue
-- rows stamped with origin_type = 'issue_draft'.
DROP TABLE IF EXISTS issue_draft;
