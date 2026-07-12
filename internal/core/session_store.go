package core

import (
	"fmt"
	"os"
	"path/filepath"

	"go.etcd.io/bbolt"
)

// sessionStore persists session-key -> session-ID mappings.
type sessionStore interface {
	Get(sessionKey string) (string, error)
	Set(sessionKey, sessionID string) error
	Delete(sessionKey string) error
	Close() error
}

const sessionBucket = "sessions"

// newSessionStore opens a bbolt database at path for persisting session IDs.
func newSessionStore(path string) (sessionStore, error) {
	if path == "" {
		return nil, fmt.Errorf("session store path is empty")
	}
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

// bboltSessionStore persists session IDs in a local bbolt database.
type bboltSessionStore struct {
	path string
	db   *bbolt.DB
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
