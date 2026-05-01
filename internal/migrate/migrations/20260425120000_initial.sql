-- +goose Up
CREATE TABLE IF NOT EXISTS daily_stats (
    date TEXT NOT NULL,
    key_name TEXT NOT NULL,
    provider_name TEXT NOT NULL,
    upstream_model TEXT NOT NULL,
    input_tokens INTEGER DEFAULT 0,
    output_tokens INTEGER DEFAULT 0,
    request_count INTEGER DEFAULT 0,
    PRIMARY KEY (date, key_name, provider_name, upstream_model)
);

CREATE INDEX IF NOT EXISTS idx_date ON daily_stats(date);

-- +goose Down
DROP INDEX IF EXISTS idx_date;
DROP TABLE IF EXISTS daily_stats;
