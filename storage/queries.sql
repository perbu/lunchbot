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

-- name: GetVacationsForDate :many
SELECT user_id
FROM vacations 
WHERE date_from <= ? AND date_to >= ?;

-- name: AddWfhRecord :exec
INSERT INTO wfh (user_id, date) 
VALUES (?, ?);

-- name: GetWfhForDate :many
SELECT user_id
FROM wfh 
WHERE date = ?;

-- name: AddClosedDay :exec
INSERT INTO closed_days (date, reason) 
VALUES (?, ?);

-- name: IsDateClosed :one
SELECT COUNT(*) as count
FROM closed_days 
WHERE date = ?;