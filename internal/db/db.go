package db

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Database struct {
	Sessions []ChatSession
	Messages []Message
	mu       sync.RWMutex
	DataDir  string
}

func New(dataDir string) *Database {
	return &Database{
		Sessions: []ChatSession{},
		Messages: []Message{},
		DataDir:  dataDir,
	}
}

func (db *Database) Load() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	// Load Sessions
	sessionsFile := filepath.Join(db.DataDir, "sessions.json")
	if _, err := os.Stat(sessionsFile); err == nil {
		data, err := os.ReadFile(sessionsFile)
		if err != nil {
			return err
		}
		if len(data) > 0 {
			if err := json.Unmarshal(data, &db.Sessions); err != nil {
				return err
			}
		}
	}

	// Load Messages
	messagesFile := filepath.Join(db.DataDir, "messages.json")
	if _, err := os.Stat(messagesFile); err == nil {
		data, err := os.ReadFile(messagesFile)
		if err != nil {
			return err
		}
		if len(data) > 0 {
			if err := json.Unmarshal(data, &db.Messages); err != nil {
				return err
			}
		}
	}

	return nil
}

func (db *Database) Save() error {
	// Lock is held by caller usually, but here we might want to lock inside.
	// To avoid deadlocks, let's assume caller handles logic or we lock here.
	// Since this is a simple app, I'll lock here.
	// But wait, if I call Save from Insert, I might double lock if Insert locks.
	// I'll make Save private or ensure I don't double lock.
	// Let's make Save internal helper `save` and public `Save` that locks.
	return db.save()
}

func (db *Database) save() error {
	// Ensure directory exists
	if err := os.MkdirAll(db.DataDir, 0755); err != nil {
		return err
	}

	sessionsFile := filepath.Join(db.DataDir, "sessions.json")
	sessionsData, err := json.MarshalIndent(db.Sessions, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(sessionsFile, sessionsData, 0644); err != nil {
		return err
	}

	messagesFile := filepath.Join(db.DataDir, "messages.json")
	messagesData, err := json.MarshalIndent(db.Messages, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(messagesFile, messagesData, 0644); err != nil {
		return err
	}

	return nil
}

func (db *Database) CreateSession() (*ChatSession, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	session := ChatSession{
		ID:        uuid.New().String(),
		CreatedAt: time.Now().UTC(),
	}

	db.Sessions = append(db.Sessions, session)
	if err := db.save(); err != nil {
		return nil, err
	}

	return &session, nil
}

func (db *Database) GetSession(id string) (*ChatSession, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	for _, s := range db.Sessions {
		if s.ID == id {
			return &s, nil
		}
	}
	return nil, fmt.Errorf("session not found")
}

func (db *Database) CreateMessage(msg Message) (*Message, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	if msg.ID == "" {
		msg.ID = uuid.New().String()
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}

	db.Messages = append(db.Messages, msg)
	if err := db.save(); err != nil {
		return nil, err
	}

	return &msg, nil
}

func (db *Database) GetMessages(sessionID string) ([]Message, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var result []Message
	for _, m := range db.Messages {
		if m.SessionID == sessionID {
			result = append(result, m)
		}
	}

	// Sort by CreatedAt ascending
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})

	return result, nil
}
