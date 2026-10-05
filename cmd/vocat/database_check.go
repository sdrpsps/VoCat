package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"time"
	"vocat/internal/store"
)

// runDatabaseCheck lets the installer verify schema compatibility with a
// candidate binary before replacing a running installation. It creates no users.
func runDatabaseCheck(args []string) error {
	flags := flag.NewFlagSet("database-check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("database", "/opt/vocat/data/vocat.db", "database path")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("usage: vocat database-check [--database path]")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := store.Open(ctx, *path)
	if err != nil {
		return err
	}
	return db.Close()
}
