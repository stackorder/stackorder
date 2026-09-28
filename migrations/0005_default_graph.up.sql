ALTER TABLE repos ADD COLUMN default_graph_id uuid REFERENCES graphs (id) ON DELETE SET NULL;
