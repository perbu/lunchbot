-- name: AddLunchRecord :exec
INSERT INTO lunches (date, user_id, verb, count, participants) 
VALUES (?, ?, ?, ?, ?);

-- name: AddVacationRecord :exec
INSERT INTO vacations (user_id, date_from, date_to) 
VALUES (?, ?, ?);

-- name: GetLunchRecordsForDate :many
SELECT id, date, user_id, verb, count, participants, created_at 
FROM lunches 
WHERE date = ?;

-- name: GetVacationCountForDate :one
SELECT COUNT(*) as count
FROM vacations 
WHERE date_from <= ? AND date_to >= ?;