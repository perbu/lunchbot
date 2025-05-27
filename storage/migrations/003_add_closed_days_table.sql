-- +goose Up
CREATE TABLE IF NOT EXISTS closed_days (
    id INTEGER PRIMARY KEY,
    date TEXT NOT NULL UNIQUE,
    reason TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_closed_days_date ON closed_days(date);

-- +goose Down
DROP TABLE IF EXISTS closed_days;
DROP INDEX IF EXISTS idx_closed_days_date;