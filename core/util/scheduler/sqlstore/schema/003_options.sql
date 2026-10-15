-- Additive metadata preserves the original schedule and fire schema. Empty
-- metadata gives legacy rows the documented defaults. Migrate remains idempotent.
CREATE TABLE IF NOT EXISTS gosched_schedule_options (
    schedule_id TEXT PRIMARY KEY,
    options_json TEXT NOT NULL DEFAULT '{}'
);
CREATE TABLE IF NOT EXISTS gosched_fire_options (
    fire_id TEXT PRIMARY KEY,
    options_json TEXT NOT NULL DEFAULT '{}'
);
