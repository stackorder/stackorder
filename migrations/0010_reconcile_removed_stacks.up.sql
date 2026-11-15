UPDATE stacks s SET removed_at = now()
FROM repos r
JOIN graphs d ON d.id = r.default_graph_id
WHERE r.id = s.repo_id
  AND s.removed_at IS NULL
  AND NOT EXISTS (
      SELECT 1 FROM graph_stacks gs
      WHERE gs.graph_id = d.id AND gs.stack_id = s.id
  )
  AND NOT EXISTS (
      SELECT 1 FROM graph_stacks gs
      JOIN graphs g ON g.id = gs.graph_id
      WHERE gs.stack_id = s.id AND g.created_at > d.created_at
  );
