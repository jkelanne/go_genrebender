package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Entry struct {
	Source     string          `json:"source"`
	MBID       string          `json:"mbid"`
	Genres     []string        `json:"genres"`
	Tags       []string        `json:"tags"`
	FetchedAt  time.Time       `json:"fetched_at"`
	APIVersion int             `json:"api_version"`
	Raw        json.RawMessage `json:"raw,omitempty"`
}

type Disk struct {
	Dir        string        // e.g. ~/.cache/genrebender
	TTL        time.Duration // e.g. 30*24*time.Hour for MBIDs
	SearchTTL  time.Duration // e.g. 7*24*time.Hour for seaches
	APIVersion int           // bumb when changing parsing logic
}

func (d *Disk) keyFile(key string) string {
	sum := sha1.Sum([]byte(strings.ToLower(strings.TrimSpace(key))))
	return filepath.Join(d.Dir, hex.EncodeToString(sum[:])+".json")
}

func (d *Disk) Get(ctx context.Context, key string, isSearch bool) (*Entry, bool, error) {
	b, err := os.ReadFile(d.keyFile(key))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}

	var e Entry
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, false, err
	}

	if e.APIVersion != d.APIVersion {
		return nil, false, nil // treat as miss
	}

	ttl := d.TTL
	if isSearch {
		ttl = d.SearchTTL
	}

	if time.Since(e.FetchedAt) > ttl {
		return &e, true, nil // stale = true
	}
	return &e, false, nil // hit
}

func (d *Disk) Put(ctx context.Context, key string, e Entry) error {
	if err := os.MkdirAll(d.Dir, 0o755); err != nil {
		return err
	}
	e.FetchedAt = time.Now().UTC()
	e.APIVersion = d.APIVersion
	data, err := json.MarshalIndent(&e, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(d.keyFile(key), data, 0o644)
}
