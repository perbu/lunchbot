package storage

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

type Storage struct {
	db *sqlite.Conn
}

type LunchRecord struct {
	ID           int      `json:"id"`
	Date         string   `json:"date"`
	UserID       string   `json:"user_id"`
	Verb         string   `json:"verb"`
	Count        int      `json:"count"`
	Participants []string `json:"participants"`
	CreatedAt    string   `json:"created_at"`
}

type VacationRecord struct {
	ID        int    `json:"id"`
	UserID    string `json:"user_id"`
	DateFrom  string `json:"date_from"`
	DateTo    string `json:"date_to"`
	CreatedAt string `json:"created_at"`
}

func New(dbPath string) (*Storage, error) {
	db, err := sqlite.OpenConn(dbPath, sqlite.OpenCreate|sqlite.OpenReadWrite)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	storage := &Storage{db: db}
	if err := storage.initDB(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	return storage, nil
}

func (s *Storage) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

func (s *Storage) initDB() error {
	createLunchesTable := `
		CREATE TABLE IF NOT EXISTS lunches (
			id INTEGER PRIMARY KEY,
			date TEXT NOT NULL,
			user_id TEXT NOT NULL,
			verb TEXT NOT NULL,
			count INTEGER NOT NULL,
			participants TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);`

	createVacationsTable := `
		CREATE TABLE IF NOT EXISTS vacations (
			id INTEGER PRIMARY KEY,
			user_id TEXT NOT NULL,
			date_from TEXT NOT NULL,
			date_to TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);`

	createIndexes := `
		CREATE INDEX IF NOT EXISTS idx_vacations_user ON vacations(user_id);
		CREATE INDEX IF NOT EXISTS idx_vacations_range ON vacations(date_from, date_to);`

	for _, query := range []string{createLunchesTable, createVacationsTable, createIndexes} {
		if err := sqlitex.Execute(s.db, query, nil); err != nil {
			return fmt.Errorf("failed to execute query: %w", err)
		}
	}

	return nil
}

func (s *Storage) AddLunchRecord(date, userID, verb string, count int, participants []string) error {
	participantsJSON, err := json.Marshal(participants)
	if err != nil {
		return fmt.Errorf("failed to marshal participants: %w", err)
	}

	query := `INSERT INTO lunches (date, user_id, verb, count, participants) VALUES (?, ?, ?, ?, ?)`
	return sqlitex.Execute(s.db, query, &sqlitex.ExecOptions{
		Args: []interface{}{date, userID, verb, count, string(participantsJSON)},
	})
}

func (s *Storage) AddVacationRecord(userID, dateFrom, dateTo string) error {
	query := `INSERT INTO vacations (user_id, date_from, date_to) VALUES (?, ?, ?)`
	return sqlitex.Execute(s.db, query, &sqlitex.ExecOptions{
		Args: []interface{}{userID, dateFrom, dateTo},
	})
}

func (s *Storage) GetLunchRecordsForDate(date string) ([]LunchRecord, error) {
	var records []LunchRecord
	
	query := `SELECT id, date, user_id, verb, count, participants, created_at FROM lunches WHERE date = ?`
	stmt := s.db.Prep(query)
	defer stmt.Finalize()
	stmt.BindText(1, date)

	for {
		hasRow, err := stmt.Step()
		if err != nil {
			return nil, fmt.Errorf("error querying lunches: %w", err)
		}
		if !hasRow {
			break
		}

		var participants []string
		participantsJSON := stmt.ColumnText(5)
		if err := json.Unmarshal([]byte(participantsJSON), &participants); err != nil {
			log.Printf("Failed to unmarshal participants: %v", err)
			participants = []string{}
		}

		record := LunchRecord{
			ID:           stmt.ColumnInt(0),
			Date:         stmt.ColumnText(1),
			UserID:       stmt.ColumnText(2),
			Verb:         stmt.ColumnText(3),
			Count:        stmt.ColumnInt(4),
			Participants: participants,
			CreatedAt:    stmt.ColumnText(6),
		}
		records = append(records, record)
	}

	return records, nil
}

func (s *Storage) GetVacationCountForDate(date string) (int, error) {
	query := `SELECT COUNT(*) FROM vacations WHERE date_from <= ? AND date_to >= ?`
	stmt := s.db.Prep(query)
	defer stmt.Finalize()
	stmt.BindText(1, date)
	stmt.BindText(2, date)

	hasRow, err := stmt.Step()
	if err != nil {
		return 0, fmt.Errorf("error querying vacations: %w", err)
	}
	if !hasRow {
		return 0, nil
	}

	return stmt.ColumnInt(0), nil
}

func (s *Storage) CalculateTotal(date string, baseline int) (int, error) {
	total := baseline

	// Get lunch changes
	records, err := s.GetLunchRecordsForDate(date)
	if err != nil {
		return 0, fmt.Errorf("failed to get lunch records: %w", err)
	}

	for _, record := range records {
		if record.Verb == "add" {
			total += record.Count
		} else if record.Verb == "detract" {
			total -= record.Count
		}
	}

	// Subtract vacations
	vacationCount, err := s.GetVacationCountForDate(date)
	if err != nil {
		return 0, fmt.Errorf("failed to get vacation count: %w", err)
	}

	total -= vacationCount

	return total, nil
}

func (s *Storage) ValidateDate(dateStr string) error {
	_, err := time.Parse("2006-01-02", dateStr)
	return err
}