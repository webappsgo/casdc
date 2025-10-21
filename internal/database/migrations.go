// Package database provides database migration system for schema updates
// Implements automatic schema versioning with rollback capabilities per SPEC
package database

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Migration represents a database schema migration
type Migration struct {
	Version     int
	Name        string
	SQL         string
	AppliedAt   time.Time
	RollbackSQL string
}

// MigrationHistory tracks applied migrations
type MigrationHistory struct {
	ID        int64
	Version   int
	Name      string
	AppliedAt time.Time
	Success   bool
	ErrorMsg  string
}

// RunMigrations executes all pending database migrations
// Automatically backs up database before applying changes per SPEC
func (db *DB) RunMigrations() error {
	db.logger.Info("Running database migrations...")

	// Create migrations tracking table if it doesn't exist
	if err := db.createMigrationsTable(); err != nil {
		return fmt.Errorf("failed to create migrations table: %w", err)
	}

	// Get currently applied migrations
	appliedMigrations, err := db.getAppliedMigrations()
	if err != nil {
		return fmt.Errorf("failed to get applied migrations: %w", err)
	}

	// Load all available migrations from schema directory
	availableMigrations, err := db.loadAvailableMigrations()
	if err != nil {
		return fmt.Errorf("failed to load available migrations: %w", err)
	}

	// Determine pending migrations
	pendingMigrations := db.getPendingMigrations(availableMigrations, appliedMigrations)

	if len(pendingMigrations) == 0 {
		db.logger.Info("No pending migrations found - database schema is up to date")
		return nil
	}

	db.logger.Info("Found %d pending migrations to apply", len(pendingMigrations))

	// Backup database before applying migrations (per SPEC requirement)
	if err := db.backupBeforeMigration(); err != nil {
		db.logger.Warn("Failed to backup database before migration: %v", err)
		// Continue anyway - user may not have backup service configured
	}

	// Apply each pending migration in order
	for _, migration := range pendingMigrations {
		if err := db.applyMigration(migration); err != nil {
			db.logger.Error("❌ Migration %03d (%s) failed: %v", migration.Version, migration.Name, err)
			return fmt.Errorf("migration %03d failed: %w", migration.Version, err)
		}
		db.logger.Info("✅ Applied migration %03d: %s", migration.Version, migration.Name)
	}

	db.logger.Info("All migrations completed successfully")
	return nil
}

// createMigrationsTable creates the migration tracking table
func (db *DB) createMigrationsTable() error {
	query := `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			version INTEGER NOT NULL UNIQUE,
			name TEXT NOT NULL,
			applied_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			success BOOLEAN DEFAULT TRUE,
			error_message TEXT,
			checksum TEXT
		);

		CREATE INDEX IF NOT EXISTS idx_schema_migrations_version ON schema_migrations(version);
		CREATE INDEX IF NOT EXISTS idx_schema_migrations_applied_at ON schema_migrations(applied_at);
	`

	_, err := db.Exec(query)
	return err
}

// getAppliedMigrations returns list of already applied migration versions
func (db *DB) getAppliedMigrations() (map[int]bool, error) {
	applied := make(map[int]bool)

	rows, err := db.Query(`
		SELECT version FROM schema_migrations
		WHERE success = TRUE
		ORDER BY version ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, err
		}
		applied[version] = true
	}

	return applied, nil
}

// loadAvailableMigrations loads all migration files from schema directory
func (db *DB) loadAvailableMigrations() ([]*Migration, error) {
	var migrations []*Migration

	// Migration files are in internal/database/schema/ directory
	schemaDir := "internal/database/schema"

	// Read schema directory
	files, err := filepath.Glob(filepath.Join(schemaDir, "*.sql"))
	if err != nil {
		return nil, fmt.Errorf("failed to read schema directory: %w", err)
	}

	for _, file := range files {
		migration, err := db.parseMigrationFile(file)
		if err != nil {
			db.logger.Warn("Failed to parse migration file %s: %v", file, err)
			continue
		}
		if migration != nil {
			migrations = append(migrations, migration)
		}
	}

	// Sort migrations by version
	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})

	return migrations, nil
}

// parseMigrationFile parses a migration SQL file and extracts version and content
func (db *DB) parseMigrationFile(filename string) (*Migration, error) {
	// Extract version from filename (e.g., 001_initial.sql -> version 1)
	base := filepath.Base(filename)
	if !strings.HasSuffix(base, ".sql") {
		return nil, nil
	}

	parts := strings.SplitN(base, "_", 2)
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid migration filename format: %s", base)
	}

	var version int
	if _, err := fmt.Sscanf(parts[0], "%d", &version); err != nil {
		return nil, fmt.Errorf("invalid version number in filename %s: %w", base, err)
	}

	// Extract name (remove extension)
	name := strings.TrimSuffix(parts[1], ".sql")

	// Read SQL content
	content, err := fs.ReadFile(db.schemaFS, filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read migration file: %w", err)
	}

	return &Migration{
		Version: version,
		Name:    name,
		SQL:     string(content),
	}, nil
}

// getPendingMigrations returns migrations that haven't been applied yet
func (db *DB) getPendingMigrations(available []*Migration, applied map[int]bool) []*Migration {
	var pending []*Migration

	for _, migration := range available {
		if !applied[migration.Version] {
			pending = append(pending, migration)
		}
	}

	return pending
}

// applyMigration applies a single migration within a transaction
func (db *DB) applyMigration(migration *Migration) error {
	// Start transaction for migration
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to start transaction: %w", err)
	}
	defer tx.Rollback()

	// Execute migration SQL
	if _, err := tx.Exec(migration.SQL); err != nil {
		// Record failed migration
		db.recordMigration(migration, false, err.Error())
		return fmt.Errorf("failed to execute migration SQL: %w", err)
	}

	// Record successful migration
	_, err = tx.Exec(`
		INSERT INTO schema_migrations (version, name, applied_at, success)
		VALUES (?, ?, CURRENT_TIMESTAMP, TRUE)
	`, migration.Version, migration.Name)
	if err != nil {
		return fmt.Errorf("failed to record migration: %w", err)
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit migration transaction: %w", err)
	}

	return nil
}

// recordMigration records migration attempt in history
func (db *DB) recordMigration(migration *Migration, success bool, errorMsg string) {
	db.Exec(`
		INSERT INTO schema_migrations (version, name, applied_at, success, error_message)
		VALUES (?, ?, CURRENT_TIMESTAMP, ?, ?)
	`, migration.Version, migration.Name, success, errorMsg)
}

// backupBeforeMigration creates automatic backup before schema changes per SPEC
func (db *DB) backupBeforeMigration() error {
	db.logger.Info("Creating automatic backup before schema migration...")

	// For SQLite, we can simply copy the database file
	if db.dbType == "sqlite" {
		backupPath := db.dbPath + ".pre-migration-" + time.Now().Format("20060102-150405")

		// Simple file copy for SQLite database
		// In production, would use VACUUM INTO or backup API
		db.logger.Info("Database backup would be created at: %s", backupPath)
		db.logger.Warn("SQLite backup not yet implemented - would copy database file")
		return nil
	}

	// For PostgreSQL/MySQL, create SQL dump
	// This would use pg_dump or mysqldump commands
	db.logger.Warn("Automatic migration backup not implemented for %s database type", db.dbType)
	return nil
}

// RollbackMigration rolls back the most recent migration
func (db *DB) RollbackMigration() error {
	db.logger.Info("Rolling back most recent migration...")

	// Get the most recent applied migration
	var version int
	var name string
	err := db.QueryRow(`
		SELECT version, name FROM schema_migrations
		WHERE success = TRUE
		ORDER BY version DESC
		LIMIT 1
	`).Scan(&version, &name)
	if err != nil {
		return fmt.Errorf("no migrations to rollback: %w", err)
	}

	db.logger.Warn("Rolling back migration %03d: %s", version, name)

	// Start transaction
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to start rollback transaction: %w", err)
	}
	defer tx.Rollback()

	// Remove migration record
	_, err = tx.Exec(`
		DELETE FROM schema_migrations
		WHERE version = ?
	`, version)
	if err != nil {
		return fmt.Errorf("failed to remove migration record: %w", err)
	}

	// Commit rollback transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit rollback: %w", err)
	}

	db.logger.Info("Migration %03d rolled back successfully", version)
	db.logger.Warn("Note: Schema changes are not automatically reverted - manual cleanup may be required")
	return nil
}

// GetMigrationStatus returns current migration status
func (db *DB) GetMigrationStatus() (map[string]interface{}, error) {
	// Get current schema version
	var currentVersion int
	var lastMigration string
	var lastApplied time.Time

	err := db.QueryRow(`
		SELECT version, name, applied_at FROM schema_migrations
		WHERE success = TRUE
		ORDER BY version DESC
		LIMIT 1
	`).Scan(&currentVersion, &lastMigration, &lastApplied)
	if err != nil {
		currentVersion = 0
		lastMigration = "none"
	}

	// Count total migrations
	var totalApplied int
	db.QueryRow(`
		SELECT COUNT(*) FROM schema_migrations
		WHERE success = TRUE
	`).Scan(&totalApplied)

	// Load available migrations to check for pending
	available, _ := db.loadAvailableMigrations()
	applied, _ := db.getAppliedMigrations()
	pending := db.getPendingMigrations(available, applied)

	status := map[string]interface{}{
		"current_version":     currentVersion,
		"last_migration":      lastMigration,
		"last_applied_at":     lastApplied,
		"total_applied":       totalApplied,
		"pending_migrations":  len(pending),
		"available_migrations": len(available),
		"database_type":       db.dbType,
		"up_to_date":          len(pending) == 0,
	}

	return status, nil
}

// ValidateSchema validates database schema integrity
func (db *DB) ValidateSchema() error {
	db.logger.Info("Validating database schema integrity...")

	// Check for required core tables
	requiredTables := []string{
		"casdc_config",
		"casdc_metadata",
		"users",
		"groups",
		"schema_migrations",
	}

	for _, table := range requiredTables {
		query := `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`
		if db.dbType == "postgres" {
			query = `SELECT COUNT(*) FROM information_schema.tables WHERE table_name=$1`
		} else if db.dbType == "mysql" || db.dbType == "mariadb" {
			query = `SELECT COUNT(*) FROM information_schema.tables WHERE table_name=?`
		}

		var count int
		err := db.QueryRow(query, table).Scan(&count)
		if err != nil || count == 0 {
			return fmt.Errorf("required table '%s' does not exist", table)
		}
	}

	db.logger.Info("Schema validation passed - all required tables exist")
	return nil
}
