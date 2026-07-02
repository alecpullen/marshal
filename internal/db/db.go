package db

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

type DB struct {
	sqlDB *sql.DB
}

func Open(path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}

	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("ping sqlite db: %w", err)
	}

	return &DB{sqlDB: sqlDB}, nil
}

func (db *DB) Close() error {
	if db.sqlDB == nil {
		return nil
	}
	return db.sqlDB.Close()
}

func (db *DB) Migrate() error {
	_, err := db.sqlDB.Exec(schema)
	if err != nil {
		return fmt.Errorf("execute database schema migrations: %w", err)
	}
	return nil
}
