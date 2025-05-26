package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

type Storage struct {
	db      *sql.DB
	queries *Queries
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
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Run migrations
	if err := goose.SetDialect("sqlite3"); err != nil {
		return nil, fmt.Errorf("failed to set dialect: %w", err)
	}

	if err := goose.Up(db, "migrations"); err != nil {
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	queries := NewQueries(db)
	storage := &Storage{
		db:      db,
		queries: queries,
	}

	return storage, nil
}

func (s *Storage) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

func (s *Storage) AddLunchRecord(date, userID, verb string, count int, participants []string) error {
	participantsJSON, err := json.Marshal(participants)
	if err != nil {
		return fmt.Errorf("failed to marshal participants: %w", err)
	}

	return s.queries.AddLunchRecord(context.Background(), AddLunchRecordParams{
		Date:         date,
		UserID:       userID,
		Verb:         verb,
		Count:        int64(count),
		Participants: string(participantsJSON),
	})
}

func (s *Storage) AddVacationRecord(userID, dateFrom, dateTo string) error {
	return s.queries.AddVacationRecord(context.Background(), AddVacationRecordParams{
		UserID:   userID,
		DateFrom: dateFrom,
		DateTo:   dateTo,
	})
}

func (s *Storage) GetLunchRecordsForDate(date string) ([]LunchRecord, error) {
	lunches, err := s.queries.GetLunchRecordsForDate(context.Background(), date)
	if err != nil {
		return nil, err
	}

	var records []LunchRecord
	for _, lunch := range lunches {
		var participants []string
		if err := json.Unmarshal([]byte(lunch.Participants), &participants); err != nil {
			log.Printf("Failed to unmarshal participants: %v", err)
			participants = []string{}
		}

		record := LunchRecord{
			ID:           int(lunch.ID),
			Date:         lunch.Date,
			UserID:       lunch.UserID,
			Verb:         lunch.Verb,
			Count:        int(lunch.Count),
			Participants: participants,
			CreatedAt:    lunch.CreatedAt.Time.Format(time.RFC3339),
		}
		records = append(records, record)
	}

	return records, nil
}

func (s *Storage) GetVacationCountForDate(date string) (int, error) {
	count, err := s.queries.GetVacationCountForDate(context.Background(), GetVacationCountForDateParams{
		DateFrom: date,
		DateTo:   date,
	})
	return int(count), err
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
