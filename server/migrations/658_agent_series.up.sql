ALTER TABLE agent ADD COLUMN series_override text NOT NULL DEFAULT '' CHECK (char_length(series_override) <= 64);
