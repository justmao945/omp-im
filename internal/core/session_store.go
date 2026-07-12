package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.etcd.io/bbolt"
)

// sessionStore persists session-key -> session-ID mappings.
type sessionStore interface {
	Get(sessionKey string) (string, error)
	Set(sessionKey, sessionID string) error
	Delete(sessionKey string) error
	Close() error
}

// newSessionStore creates the appropriate store implementation for the given
// path. Paths ending in .db use bbolt; otherwise a JSON file is used.
func newSessionStore(path string) (sessionStore, error) {
	if path == "" {
		return nil, fmt.Errorf("session store path is empty")
	}
	if strings.HasSuffix(strings.ToLower(path), ".db") {
		return newBboltSessionStore(path)
	}
	return newJSONFileSessionStore(path), nil
}

// jsonFileSessionStore stores sessions as a single JSON object on disk.
type jsonFileSessionStore struct {
	path string
}

func newJSONFileSessionStore(path string) *jsonFileSessionStore {
	return &jsonFileSessionStore{path: path}
}

func (s *jsonFileSessionStore) load() (map[string]string, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("read session store: %w", err)
	}
	if len(data) == 0 {
		return map[string]string{}, nil
	}
	var store map[string]string
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, fmt.Errorf("parse session store: %w", err)
	}
	return store, nil
}

func (s *jsonFileSessionStore) save(m map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create session store dir: %w", err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session store: %w", err)
	}
	if err := os.WriteFile(s.path, data, 0o600); err != nil {
		return fmt.Errorf("write session store: %w", err)
	}
	return nil
}

func (s *jsonFileSessionStore) Get(sessionKey string) (string, error) {
	m, err := s.load()
	if err != nil {
		return "", err
	}
	return m[sessionKey], nil
}

func (s *jsonFileSessionStore) Set(sessionKey, sessionID string) error {
	m, err := s.load()
	if err != nil {
		return err
	}
	m[sessionKey] = sessionID
	return s.save(m)
}

func (s *jsonFileSessionStore) Delete(sessionKey string) error {
	m, err := s.load()
	if err != nil {
		return err
	}
	delete(m, sessionKey)
	return s.save(m)
}

func (s *jsonFileSessionStore) Close() error { return nil }

// bboltSessionStore persists session IDs in a local bbolt database.
type bboltSessionStore struct {
	path string
	db   *bbolt.DB
}

const sessionBucket = "sessions"

func newBboltSessionStore(path string) (*bboltSessionStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create session store dir: %w", err)
	}
	db, err := bbolt.Open(path, 0o600, nil)
	if err != nil {
		return nil, fmt.Errorf("open bbolt session store: %w", err)
	}
	if err := db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(sessionBucket))
		return err
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init bbolt session bucket: %w", err)
	}
	return &bboltSessionStore{path: path, db: db}, nil
}

func (s *bboltSessionStore) Get(sessionKey string) (string, error) {
	var sessionID string
	if err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(sessionBucket))
		if b == nil {
			return nil
		}
		v := b.Get([]byte(sessionKey))
		if v != nil {
			sessionID = string(v)
		}
		return nil
	}); err != nil {
		return "", fmt.Errorf("get bbolt session: %w", err)
	}
	return sessionID, nil
}

func (s *bboltSessionStore) Set(sessionKey, sessionID string) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(sessionBucket))
		return b.Put([]byte(sessionKey), []byte(sessionID))
	})
}

func (s *bboltSessionStore) Delete(sessionKey string) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(sessionBucket))
		return b.Delete([]byte(sessionKey))
	})
}

func (s *bboltSessionStore) Close() error {
	return s.db.Close()
}
