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
package cmd

import (
	"database/sql"
	"log"
	"os"

	_ "github.com/mattn/go-sqlite3"
	"github.com/sottey/tududimport/internal/models"
	"github.com/sottey/tududimport/internal/utils"
	"github.com/spf13/cobra"
)

var cfg models.Config

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "tududimport",
	Short: "Import a file system tree into Tududi's db directly",
	Run: func(cmd *cobra.Command, args []string) {
		db, err := sql.Open("sqlite3", cfg.DBPath)
		if err != nil {
			log.Fatalf("open db: %v", err)
		}
		defer db.Close()

		if err := db.Ping(); err != nil {
			log.Fatalf("ping db: %v", err)
		}

		log.Printf("Connected to DB: %s\n", cfg.DBPath)

		notes, err := utils.DiscoverNotes(cfg)
		if err != nil {
			log.Fatalf("discover notes: %v", err)
		}
		log.Printf("Discovered %d markdown files\n", len(notes))

		tagCache := make(map[string]int64) // key: name|userID

		tx, err := db.Begin()
		if err != nil {
			log.Fatalf("begin tx: %v", err)
		}
		defer func() {
			if cfg.DryRun {
				log.Println("DRY-RUN: rolling back transaction")
				_ = tx.Rollback()
			}
		}()

		for i, n := range notes {
			log.Printf("[%d/%d] Importing %s\n", i+1, len(notes), n.Path)

			noteID, err := utils.InsertNote(tx, cfg, n)
			if err != nil {
				log.Fatalf("insert note (%s): %v", n.Path, err)
			}

			uniqueTags := utils.UniqueStrings(n.Tags)
			for _, t := range uniqueTags {
				tagID, err := utils.GetOrCreateTag(tx, cfg, tagCache, t)
				if err != nil {
					log.Fatalf("get/create tag (%s): %v", t, err)
				}
				if err := utils.LinkNoteTag(tx, noteID, tagID); err != nil {
					log.Fatalf("link note/tag (%d,%d): %v", noteID, tagID, err)
				}
			}
		}

		if cfg.DryRun {
			log.Println("DRY-RUN complete, transaction rolled back.")
			return
		}

		if err := tx.Commit(); err != nil {
			log.Fatalf("commit tx: %v", err)
		}

		log.Println("Import complete.")
	},
}

func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&cfg.DBPath, "db", "d", "", "Path to Tududi SQLite DB (required)")
	rootCmd.PersistentFlags().StringVarP(&cfg.Root, "root", "r", "", "Root directory of markdown files (required)")
	rootCmd.PersistentFlags().IntVarP(&cfg.UserID, "user-id", "u", 1, "User ID to assign to imported notes and tags (Defaults to 1)")
	rootCmd.PersistentFlags().IntVarP(&cfg.ProjectID, "project-id", "p", -1, "Project ID to assign to imported notes (-1 or omitted means no project)")
	rootCmd.PersistentFlags().BoolVarP(&cfg.DryRun, "no-commit", "n", true, "Dry run (do not commit writes) (Defaults to true)")
	rootCmd.PersistentFlags().BoolVarP(&cfg.TagFromFolders, "tag-from-folders", "f", true, "Create tags from folder hierarchy under root (Defaults to true)")
	rootCmd.PersistentFlags().BoolVarP(&cfg.TagFromHashtags, "tag-from-hashtags", "t", true, "Create tags from inline #tags (Defaults to true)")

	rootCmd.MarkPersistentFlagRequired("db")
	rootCmd.MarkPersistentFlagRequired("root")
}
