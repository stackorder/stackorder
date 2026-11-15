ALTER TABLE events ADD COLUMN trace_context jsonb;
ALTER TABLE jobs ADD COLUMN trace_context jsonb;
