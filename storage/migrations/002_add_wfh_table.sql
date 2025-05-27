-- +goose Up
CREATE TABLE IF NOT EXISTS wfh (
    id INTEGER PRIMARY KEY,
    user_id TEXT NOT NULL,
    date TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_wfh_user ON wfh(user_id);
CREATE INDEX IF NOT EXISTS idx_wfh_date ON wfh(date);

-- +goose Down
DROP TABLE IF EXISTS wfh;
DROP INDEX IF EXISTS idx_wfh_user;
DROP INDEX IF EXISTS idx_wfh_date;