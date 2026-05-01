-- +goose Up
ALTER TABLE daily_stats ADD COLUMN latency_ms INTEGER DEFAULT 0;

-- +goose Down
-- SQLite does not support DROP COLUMN in older versions
-- For modern SQLite (3.35.0+), we can use:
-- ALTER TABLE daily_stats DROP COLUMN latency_ms;
