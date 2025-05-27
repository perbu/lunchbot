package storage

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var embedMigrations embed.FS

type Storage struct {
	db      *sql.DB
	queries *Queries
	logger  *slog.Logger
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

func NewStorage(dbPath string, logger *slog.Logger) (*Storage, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Run migrations
	if err := goose.SetDialect("sqlite3"); err != nil {
		return nil, fmt.Errorf("failed to set dialect: %w", err)
	}

	goose.SetBaseFS(embedMigrations)
	if err := goose.Up(db, "migrations"); err != nil {
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	queries := New(db)
	storage := &Storage{
		db:      db,
		queries: queries,
		logger:  logger,
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
			s.logger.Warn("Failed to unmarshal participants", "error", err)
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

func (s *Storage) GetVacationsForDate(date string) ([]string, error) {
	return s.queries.GetVacationsForDate(context.Background(), GetVacationsForDateParams{
		DateFrom: date,
		DateTo:   date,
	})
}

func (s *Storage) AddWfhRecord(userID, date string) error {
	return s.queries.AddWfhRecord(context.Background(), AddWfhRecordParams{
		UserID: userID,
		Date:   date,
	})
}

func (s *Storage) GetWfhForDate(date string) ([]string, error) {
	return s.queries.GetWfhForDate(context.Background(), date)
}

func (s *Storage) AddClosedDay(date, reason string) error {
	return s.queries.AddClosedDay(context.Background(), AddClosedDayParams{
		Date:   date,
		Reason: sql.NullString{String: reason, Valid: reason != ""},
	})
}

func (s *Storage) IsDateClosed(date string) (bool, error) {
	parsedTime, err := time.Parse("2006-01-02", date)
	if err != nil {
		return false, fmt.Errorf("invalid date format: %w", err)
	}

	// Check if it's a weekend (Saturday or Sunday)
	if parsedTime.Weekday() == time.Saturday || parsedTime.Weekday() == time.Sunday {
		return true, nil
	}

	// Check if date is explicitly marked as closed
	count, err := s.queries.IsDateClosed(context.Background(), date)
	if err != nil {
		return false, err
	}

	return count > 0, nil
}

func (s *Storage) CalculateTotal(date string, baseline int) (int, error) {
	// Check if date is closed
	isClosed, err := s.IsDateClosed(date)
	if err != nil {
		return 0, fmt.Errorf("failed to check if date is closed: %w", err)
	}
	if isClosed {
		return 0, nil
	}

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

	// Subtract WFH
	wfhUsers, err := s.GetWfhForDate(date)
	if err != nil {
		return 0, fmt.Errorf("failed to get WFH users: %w", err)
	}

	total -= len(wfhUsers)

	return total, nil
}

func (s *Storage) ValidateDate(dateStr string) error {
	_, err := time.Parse("2006-01-02", dateStr)
	return err
}
