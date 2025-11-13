/*
Copyright © 2025 sottey

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE.
*/
package utils

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sottey/tududimport/internal/models"
)

var tagRegex = regexp.MustCompile(`#([A-Za-z0-9_\-]+)`)

// discoverNotes walks the root dir and returns Note structs for each .md file.
func DiscoverNotes(cfg models.Config) ([]models.Note, error) {
	var notes []models.Note

	err := filepath.Walk(cfg.Root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(info.Name()), ".md") {
			return nil
		}

		n, err := parseMarkdownNote(cfg, path, info)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		notes = append(notes, n)
		return nil
	})

	return notes, err
}

// parseMarkdownNote reads a .md file, extracts title, body, tags, and file timestamps.
func parseMarkdownNote(cfg models.Config, path string, info os.FileInfo) (models.Note, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return models.Note{}, err
	}
	text := string(data)
	lines := strings.Split(text, "\n")

	title := ""
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
			break
		}
	}
	if title == "" {
		base := filepath.Base(path)
		title = strings.TrimSuffix(base, filepath.Ext(base))
	}

	var tags []string

	// Inline #tags
	if cfg.TagFromHashtags {
		matches := tagRegex.FindAllStringSubmatch(text, -1)
		for _, m := range matches {
			if len(m) > 1 {
				tags = append(tags, m[1])
			}
		}
	}

	// Folder-based tags: *all* folders under root, e.g. cottage/foo/bar/file.md => cottage, foo, bar
	if cfg.TagFromFolders {
		rel, err := filepath.Rel(cfg.Root, path)
		if err == nil {
			dirPart := filepath.Dir(rel)
			if dirPart != "." {
				parts := strings.Split(dirPart, string(os.PathSeparator))
				for _, p := range parts {
					slug := slugify(p)
					if slug != "" {
						tags = append(tags, slug)
					}
				}
			}
		}
	}

	// File timestamps (using ModTime for both created/updated)
	modTime := info.ModTime()

	return models.Note{
		Title:     title,
		Body:      text,
		Tags:      tags,
		Path:      path,
		CreatedAt: modTime,
		UpdatedAt: modTime,
	}, nil
}

// insertNote inserts into the notes table and returns the inserted note ID.
func InsertNote(tx *sql.Tx, cfg models.Config, n models.Note) (int64, error) {
	createdStr := n.CreatedAt.UTC().Format("2006-01-02 15:04:05.000 +00:00")
	updatedStr := n.UpdatedAt.UTC().Format("2006-01-02 15:04:05.000 +00:00")
	uid := GenerateID() // uuid.New().String()

	var (
		sqlStr string
		args   []interface{}
	)

	if cfg.ProjectID >= 0 {
		// with project_id
		sqlStr = `
			INSERT INTO notes (uid, title, content, user_id, project_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`
		args = []interface{}{uid, n.Title, n.Body, cfg.UserID, cfg.ProjectID, createdStr, updatedStr}
	} else {
		// without project_id
		sqlStr = `
			INSERT INTO notes (uid, title, content, user_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
		`
		args = []interface{}{uid, n.Title, n.Body, cfg.UserID, createdStr, updatedStr}
	}

	res, err := tx.Exec(sqlStr, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// getOrCreateTag returns an existing tag id or creates a new one if needed.
func GetOrCreateTag(tx *sql.Tx, cfg models.Config, cache map[string]int64, name string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, fmt.Errorf("empty tag name")
	}

	cacheKey := fmt.Sprintf("%s|%d", name, cfg.UserID)
	if id, ok := cache[cacheKey]; ok {
		return id, nil
	}

	// Try to find existing tag for this user
	selectSQL := `
		SELECT id FROM tags
		WHERE name = ? AND user_id = ?
		LIMIT 1
	`
	var existingID int64
	err := tx.QueryRow(selectSQL, name, cfg.UserID).Scan(&existingID)
	if err == nil {
		cache[cacheKey] = existingID
		return existingID, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}

	// Insert new tag
	now := time.Now().UTC().Format("2006-01-02 15:04:05.000 +00:00")
	uid := GenerateID()

	insertSQL := `
		INSERT INTO tags (uid, name, user_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`
	res, err := tx.Exec(insertSQL, uid, name, cfg.UserID, now, now)
	if err != nil {
		return 0, err
	}
	newID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	cache[cacheKey] = newID
	return newID, nil
}

// linkNoteTag inserts into the notes_tags intersection table.
// INSERT OR IGNORE so re-running the importer won't blow up on duplicates.
func LinkNoteTag(tx *sql.Tx, noteID, tagID int64) error {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	insertSQL := `
		INSERT OR IGNORE INTO notes_tags (note_id, tag_id, created_at, updated_at)
		VALUES (?, ?, ?, ?)
	`
	_, err := tx.Exec(insertSQL, noteID, tagID, now, now)
	return err
}

func UniqueStrings(in []string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}

// slugify turns "Server Notes" -> "server-notes"
func slugify(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.ToLower(s)
	// Replace spaces and some separators with hyphen
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "_", "-")
	// Remove characters that aren't letters, numbers, or hyphen
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func GenerateID() string {
	const charset = "0123456789abcdefghijklmnopqrstuvwxyz"
	const length = 15
	result := make([]byte, length)
	for i := range result {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		result[i] = charset[n.Int64()]
	}
	return string(result)
}
