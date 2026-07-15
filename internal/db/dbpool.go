package db

import (
	"database/sql"
	"fmt"
)

// OpenWithPool opens a SQLite database with WAL mode, a dedicated writer
// connection (MaxOpenConns=1) and a separate read pool of readPoolSize
// connections. Pass readPoolSize < 1 to get the minimum of 1.
func OpenWithPool(path string, readPoolSize int) (*DB, error) {
	if readPoolSize < 1 {
		readPoolSize = 1
	}

	sqlDB, err := openOneConnection(path)
	if err != nil {
		return nil, err
	}

	// Read pool connections are read-only — they never write to the database,
	// so foreign key enforcement (PRAGMA foreign_keys = ON) is intentionally
	// omitted. Foreign keys are enforced by the single writer connection.
	readDB, err := sql.Open("sqlite", path)
	if err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("open read pool: %w", err)
	}
	readDB.SetMaxOpenConns(readPoolSize)
	readDB.SetMaxIdleConns(readPoolSize)
	if _, err := readDB.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		sqlDB.Close()
		readDB.Close()
		return nil, fmt.Errorf("set read busy_timeout: %w", err)
	}

	return &DB{sqlDB: sqlDB, readDB: readDB, locks: NewProjectLocks()}, nil
}

// openOneConnection opens a single *sql.DB pinned to one connection and runs
// the standard set of SQLite pragmas (busy_timeout, foreign_keys, WAL, synchronous).
func openOneConnection(path string) (*sql.DB, error) {
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	pragmas := []string{
		"PRAGMA busy_timeout = 5000",
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
	}
	for _, p := range pragmas {
		if _, err := sqlDB.Exec(p); err != nil {
			sqlDB.Close()
			return nil, fmt.Errorf("%s: %w", p, err)
		}
	}

	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	return sqlDB, nil
}
