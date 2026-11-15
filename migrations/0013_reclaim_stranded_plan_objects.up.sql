CREATE INDEX run_stacks_stranded_idx ON run_stacks (updated_at) WHERE plan_text IS NULL AND plan_url <> '';
