package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/bcrypt"
)

// DefaultPath is where the config lives when no explicit path is given.
const DefaultPath = "/etc/veilbridge/config.json"

// Store loads and saves the config document at a fixed path. Writes are atomic:
// a temp file in the same directory is written, fsync'd, then renamed over the
// target, so a crash mid-write never corrupts the live config (D-4).
type Store struct {
	path string
}

// NewStore returns a Store backed by path (DefaultPath if empty).
func NewStore(path string) *Store {
	if path == "" {
		path = DefaultPath
	}
	return &Store{path: path}
}

// Path returns the file path this store uses.
func (s *Store) Path() string { return s.path }

// Load reads the document. If the file does not exist, it returns Default() and
// no error — first run is not a failure.
func (s *Store) Load() (*Document, error) {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", s.path, err)
	}
	var d Document
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", s.path, err)
	}
	if d.Version == "" {
		d.Version = Version
	}
	return &d, nil
}

// Save writes the document atomically (temp file + rename within the same dir).
func (s *Store) Save(d *Document) error {
	if d == nil {
		return errors.New("config: cannot save nil document")
	}
	if d.Version == "" {
		d.Version = Version
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: mkdir %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("config: temp file: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail before the rename.
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("config: chmod temp: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("config: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("config: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: close temp: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("config: rename into place: %w", err)
	}
	return nil
}

// SetPassword stores the bcrypt hash of password in the document's settings.
func (d *Document) SetPassword(password string) error {
	if password == "" {
		return errors.New("config: empty password")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("config: hash password: %w", err)
	}
	d.Settings.PasswordHash = string(h)
	return nil
}

// VerifyPassword reports whether password matches the stored hash. It returns
// false (no error) when no password has been set yet.
func (d *Document) VerifyPassword(password string) bool {
	if d.Settings.PasswordHash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(d.Settings.PasswordHash), []byte(password)) == nil
}
