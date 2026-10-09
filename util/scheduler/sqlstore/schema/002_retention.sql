-- Compact permanent occurrence fence: never removed with a schedule or fire.
-- After pruning, occurrences at or before through_at cannot be materialized.
CREATE TABLE IF NOT EXISTS gosched_pruned (
    schedule_id TEXT PRIMARY KEY,
    through_at TEXT NOT NULL
);
