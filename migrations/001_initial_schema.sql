-- +goose Up
CREATE TABLE IF NOT EXISTS lunches (
    id INTEGER PRIMARY KEY,
    date TEXT NOT NULL,
    user_id TEXT NOT NULL,
    verb TEXT NOT NULL,
    count INTEGER NOT NULL,
    participants TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS vacations (
    id INTEGER PRIMARY KEY,
    user_id TEXT NOT NULL,
    date_from TEXT NOT NULL,
    date_to TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_vacations_user ON vacations(user_id);
CREATE INDEX IF NOT EXISTS idx_vacations_range ON vacations(date_from, date_to);

-- +goose Down
DROP TABLE IF EXISTS lunches;
DROP TABLE IF EXISTS vacations;
DROP INDEX IF EXISTS idx_vacations_user;
DROP INDEX IF EXISTS idx_vacations_range;