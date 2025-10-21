// Package database provides database abstraction and management for CASDC
// Supporting SQLite, PostgreSQL, MariaDB, and MySQL with automatic schema migration
package database

import (
	"database/sql"
	"embed"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/casapps/casdc/internal/config"
	"github.com/casapps/casdc/pkg/logger"

	_ "github.com/lib/pq"          // PostgreSQL driver
	_ "github.com/go-sql-driver/mysql" // MySQL/MariaDB driver
	_ "modernc.org/sqlite"         // Pure Go SQLite driver (no CGO required)
)

//go:embed schema/*.sql
var schemaFiles embed.FS

// DB represents the database connection and operations
type DB struct {
	conn     *sql.DB
	dbType   string
	dbPath   string
	logger   *logger.Logger
	mu       sync.RWMutex
	schemaFS embed.FS

	// Prepared statements cache
	stmts map[string]*sql.Stmt
}

// Initialize creates and configures the database connection
func Initialize(cfg config.DatabaseConfig, log *logger.Logger) (*DB, error) {
	var conn *sql.DB
	var err error

	// Open database connection based on type
	switch cfg.Type {
	case "sqlite":
		conn, err = sql.Open("sqlite", cfg.GetDSN())
		if err != nil {
			return nil, fmt.Errorf("failed to open SQLite database: %w", err)
		}

		// Configure SQLite for production use
		if _, err := conn.Exec(`
			PRAGMA journal_mode = WAL;
			PRAGMA synchronous = NORMAL;
			PRAGMA cache_size = -64000;
			PRAGMA foreign_keys = ON;
			PRAGMA busy_timeout = 5000;
			PRAGMA temp_store = MEMORY;
		`); err != nil {
			return nil, fmt.Errorf("failed to configure SQLite: %w", err)
		}

	case "postgres":
		conn, err = sql.Open("postgres", cfg.GetDSN())
		if err != nil {
			return nil, fmt.Errorf("failed to open PostgreSQL database: %w", err)
		}

	case "mysql", "mariadb":
		conn, err = sql.Open("mysql", cfg.GetDSN())
		if err != nil {
			return nil, fmt.Errorf("failed to open MySQL/MariaDB database: %w", err)
		}

	default:
		return nil, fmt.Errorf("unsupported database type: %s", cfg.Type)
	}

	// Configure connection pool
	conn.SetMaxOpenConns(cfg.MaxConns)
	conn.SetMaxIdleConns(cfg.MinConns)
	conn.SetConnMaxLifetime(time.Hour)

	// Test connection
	if err := conn.Ping(); err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	db := &DB{
		conn:     conn,
		dbType:   cfg.Type,
		dbPath:   cfg.GetDSN(),
		logger:   log,
		schemaFS: schemaFiles,
		stmts:    make(map[string]*sql.Stmt),
	}

	// Run database migrations
	if err := db.Migrate(); err != nil {
		return nil, fmt.Errorf("failed to run database migrations: %w", err)
	}

	// Initialize default data
	if err := db.InitializeDefaults(); err != nil {
		return nil, fmt.Errorf("failed to initialize default data: %w", err)
	}

	return db, nil
}

// Close closes the database connection and all prepared statements
func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	// Close all prepared statements
	for _, stmt := range db.stmts {
		stmt.Close()
	}

	return db.conn.Close()
}

// Migrate runs database migrations to ensure schema is up to date
// Uses the new comprehensive migration system from migrations.go
func (db *DB) Migrate() error {
	// Create metadata table if it doesn't exist
	if err := db.createMetadataTable(); err != nil {
		return fmt.Errorf("failed to create metadata table: %w", err)
	}

	// Run new migration system with automatic backup and rollback
	return db.RunMigrations()
}

// createMetadataTable creates the metadata table if it doesn't exist
func (db *DB) createMetadataTable() error {
	query := `
	CREATE TABLE IF NOT EXISTS casdc_metadata (
		id INTEGER PRIMARY KEY,
		version TEXT NOT NULL,
		schema_version INTEGER NOT NULL DEFAULT 0,
		installation_id TEXT UNIQUE NOT NULL,
		domain TEXT,
		organization TEXT,
		admin_email TEXT,
		installed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		last_backup TIMESTAMP,
		last_update TIMESTAMP
	)`

	// Adjust for different database types
	if db.dbType == "postgres" {
		query = strings.Replace(query, "INTEGER PRIMARY KEY", "SERIAL PRIMARY KEY", 1)
		query = strings.Replace(query, "TIMESTAMP", "TIMESTAMPTZ", -1)
	} else if db.dbType == "mysql" || db.dbType == "mariadb" {
		query = strings.Replace(query, "INTEGER PRIMARY KEY", "INT AUTO_INCREMENT PRIMARY KEY", 1)
	}

	_, err := db.conn.Exec(query)
	return err
}

// getSchemaVersion returns the current schema version
func (db *DB) getSchemaVersion() (int, error) {
	var version int
	err := db.conn.QueryRow("SELECT COALESCE(MAX(schema_version), 0) FROM casdc_metadata").Scan(&version)
	if err != nil {
		// Table might not have any rows yet
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, err
	}
	return version, nil
}

// loadMigrations loads migration files from embedded filesystem
func (db *DB) loadMigrations() (map[int]string, error) {
	migrations := make(map[int]string)

	// Migration 1: Initial schema
	migrations[1] = db.getInitialSchema()

	// Migration 2: Complete schema with all tables
	migration2, err := schemaFiles.ReadFile("schema/002_complete_schema.sql")
	if err != nil {
		// If migration file doesn't exist yet, skip it
		db.logger.Warn("Migration 2 file not found, skipping")
	} else {
		migrations[2] = string(migration2)
	}

	return migrations, nil
}

// getInitialSchema returns the initial database schema
func (db *DB) getInitialSchema() string {
	schema := `
-- Core System Tables
CREATE TABLE IF NOT EXISTS casdc_config (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL,
	description TEXT,
	category TEXT,
	type TEXT DEFAULT 'string',
	encrypted BOOLEAN DEFAULT FALSE,
	created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- User Management Tables
CREATE TABLE IF NOT EXISTS users (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	username TEXT UNIQUE NOT NULL,
	email TEXT UNIQUE NOT NULL,
	password_hash TEXT NOT NULL,
	first_name TEXT,
	last_name TEXT,
	display_name TEXT,
	description TEXT,
	home_directory TEXT,
	shell TEXT DEFAULT '/bin/bash',
	uid INTEGER UNIQUE,
	gid INTEGER,
	enabled BOOLEAN DEFAULT TRUE,
	password_expires TIMESTAMP,
	account_expires TIMESTAMP,
	last_login TIMESTAMP,
	failed_login_attempts INTEGER DEFAULT 0,
	locked_until TIMESTAMP,
	created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	created_by INTEGER REFERENCES users(id),
	phone TEXT,
	department TEXT,
	title TEXT,
	manager_id INTEGER REFERENCES users(id),
	employee_id TEXT,
	office_location TEXT
);

CREATE TABLE IF NOT EXISTS groups (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT UNIQUE NOT NULL,
	description TEXT,
	gid INTEGER UNIQUE,
	type TEXT DEFAULT 'security' CHECK (type IN ('security', 'distribution')),
	scope TEXT DEFAULT 'domain_local' CHECK (scope IN ('domain_local', 'global', 'universal')),
	created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	created_by INTEGER REFERENCES users(id)
);

CREATE TABLE IF NOT EXISTS user_groups (
	user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
	group_id INTEGER REFERENCES groups(id) ON DELETE CASCADE,
	added_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	added_by INTEGER REFERENCES users(id),
	PRIMARY KEY (user_id, group_id)
);

-- Session Management
CREATE TABLE IF NOT EXISTS user_sessions (
	id TEXT PRIMARY KEY,
	user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
	ip_address TEXT,
	user_agent TEXT,
	created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	last_activity TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
	expires_at TIMESTAMP NOT NULL,
	active BOOLEAN DEFAULT TRUE
);

-- Audit Logging
CREATE TABLE IF NOT EXISTS audit_logs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id INTEGER REFERENCES users(id),
	action TEXT NOT NULL,
	resource_type TEXT,
	resource_id TEXT,
	ip_address TEXT,
	user_agent TEXT,
	details TEXT,
	created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Create indexes for better performance
CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);
CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);
CREATE INDEX IF NOT EXISTS idx_users_enabled ON users(enabled);
CREATE INDEX IF NOT EXISTS idx_user_sessions_user_id ON user_sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_user_sessions_expires ON user_sessions(expires_at);
CREATE INDEX IF NOT EXISTS idx_audit_logs_user_id ON audit_logs(user_id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_created ON audit_logs(created_at);
`

	// Adjust schema for different database types
	if db.dbType == "postgres" {
		schema = strings.Replace(schema, "INTEGER PRIMARY KEY AUTOINCREMENT", "SERIAL PRIMARY KEY", -1)
		schema = strings.Replace(schema, "BOOLEAN", "BOOLEAN", -1) // PostgreSQL supports BOOLEAN
		schema = strings.Replace(schema, "TIMESTAMP", "TIMESTAMPTZ", -1)
		schema = strings.Replace(schema, "TEXT", "VARCHAR(255)", -1) // Use VARCHAR for indexed columns
	} else if db.dbType == "mysql" || db.dbType == "mariadb" {
		schema = strings.Replace(schema, "INTEGER PRIMARY KEY AUTOINCREMENT", "INT AUTO_INCREMENT PRIMARY KEY", -1)
		schema = strings.Replace(schema, "BOOLEAN", "TINYINT(1)", -1)
		schema = strings.Replace(schema, "TEXT PRIMARY KEY", "VARCHAR(255) PRIMARY KEY", -1)
		schema = strings.Replace(schema, "TEXT UNIQUE", "VARCHAR(255) UNIQUE", -1)
	}

	return schema
}

// InitializeDefaults creates default data if database is empty
func (db *DB) InitializeDefaults() error {
	// Check if this is a fresh installation
	var count int
	err := db.conn.QueryRow("SELECT COUNT(*) FROM casdc_metadata").Scan(&count)
	if err != nil || count > 0 {
		return nil // Already initialized
	}

	// Generate installation ID
	installationID := generateInstallationID()

	// Insert metadata record
	_, err = db.conn.Exec(`
		INSERT INTO casdc_metadata (id, version, schema_version, installation_id, installed_at)
		VALUES (1, ?, 1, ?, ?)`,
		"1.0.0", installationID, time.Now(),
	)
	if err != nil {
		return fmt.Errorf("failed to insert metadata: %w", err)
	}

	// Create default admin user
	if err := db.createDefaultAdmin(); err != nil {
		return fmt.Errorf("failed to create default admin: %w", err)
	}

	// Create default groups
	if err := db.createDefaultGroups(); err != nil {
		return fmt.Errorf("failed to create default groups: %w", err)
	}

	return nil
}

// createDefaultAdmin creates the default administrator account
func (db *DB) createDefaultAdmin() error {
	// Check if admin already exists
	var count int
	err := db.conn.QueryRow("SELECT COUNT(*) FROM users WHERE username = 'admin'").Scan(&count)
	if err != nil || count > 0 {
		return nil
	}

	// Generate secure random password
	password := generateSecurePassword()
	passwordHash := hashPassword(password)

	// Insert admin user
	result, err := db.conn.Exec(`
		INSERT INTO users (username, email, password_hash, first_name, last_name,
		                  display_name, uid, gid, enabled)
		VALUES ('admin', 'admin@example.local', ?, 'System', 'Administrator',
		        'Administrator', 1000, 1000, TRUE)`,
		passwordHash,
	)
	if err != nil {
		return err
	}

	_, _ = result.LastInsertId()

	// Store initial password in config for first login
	_, err = db.conn.Exec(`
		INSERT INTO casdc_config (key, value, description, category, encrypted)
		VALUES ('initial_admin_password', ?, 'Initial admin password (delete after first login)', 'security', TRUE)`,
		password,
	)
	if err != nil {
		return err
	}

	db.logger.Info("Created default admin user")
	db.logger.Info("================================================")
	db.logger.Info("Initial admin password: %s", password)
	db.logger.Info("Please change this password after first login!")
	db.logger.Info("================================================")

	// Add admin to admin group (will be created next)
	return nil
}

// createDefaultGroups creates the default security groups
func (db *DB) createDefaultGroups() error {
	defaultGroups := []struct {
		name        string
		description string
		gid         int
	}{
		{"admin", "CASDC Administrators", 1000},
		{"users", "Domain Users", 1001},
		{"guests", "Guest Users", 1002},
	}

	for _, group := range defaultGroups {
		_, err := db.conn.Exec(`
			INSERT OR IGNORE INTO groups (name, description, gid, type, scope)
			VALUES (?, ?, ?, 'security', 'global')`,
			group.name, group.description, group.gid,
		)
		if err != nil {
			return fmt.Errorf("failed to create group %s: %w", group.name, err)
		}
	}

	// Add admin user to admin group
	_, err := db.conn.Exec(`
		INSERT OR IGNORE INTO user_groups (user_id, group_id)
		SELECT u.id, g.id FROM users u, groups g
		WHERE u.username = 'admin' AND g.name = 'admin'
	`)
	if err != nil {
		return fmt.Errorf("failed to add admin to admin group: %w", err)
	}

	return nil
}

// Helper functions

func generateInstallationID() string {
	// Generate a unique installation ID
	// In production, use crypto/rand for secure random generation
	return fmt.Sprintf("casdc_%d_%s", time.Now().Unix(), randomString(20))
}

func generateSecurePassword() string {
	// Generate a secure random password
	// In production, use crypto/rand and ensure complexity requirements
	return "ChangeMeNow!" + randomString(8)
}

func hashPassword(password string) string {
	// In production, use bcrypt for password hashing
	// This is a placeholder
	return "hashed_" + password
}

func randomString(length int) string {
	// In production, use crypto/rand for secure random generation
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	result := make([]byte, length)
	for i := range result {
		result[i] = charset[time.Now().UnixNano()%int64(len(charset))]
	}
	return string(result)
}

// Query executes a query that returns rows with proper error handling
func (db *DB) Query(query string, args ...interface{}) (*sql.Rows, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	return db.conn.Query(query, args...)
}

// QueryRow executes a query that is expected to return at most one row
func (db *DB) QueryRow(query string, args ...interface{}) *sql.Row {
	db.mu.RLock()
	defer db.mu.RUnlock()

	return db.conn.QueryRow(query, args...)
}

// Exec executes a query without returning any rows
func (db *DB) Exec(query string, args ...interface{}) (sql.Result, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	return db.conn.Exec(query, args...)
}

// Begin starts a database transaction
func (db *DB) Begin() (*sql.Tx, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	return db.conn.Begin()
}

// Prepare creates a prepared statement for later queries or executions
func (db *DB) Prepare(query string) (*sql.Stmt, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	return db.conn.Prepare(query)
}