-- Migration 005: Kerberos authentication support
-- Adds Kerberos principal and ticket management for Windows domain join

-- Kerberos Principals (users and services)
CREATE TABLE IF NOT EXISTS kerberos_principals (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    principal TEXT NOT NULL,
    realm TEXT NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('user', 'service')),
    key_data TEXT NOT NULL,  -- Base64-encoded encryption key
    kvno INTEGER DEFAULT 1,  -- Key version number
    enabled BOOLEAN DEFAULT TRUE,
    description TEXT,
    last_auth DATETIME,
    failed_auth_count INTEGER DEFAULT 0,
    locked_until DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(principal, realm)
);

-- Kerberos Service Keys (multiple encryption types per principal)
CREATE TABLE IF NOT EXISTS kerberos_service_keys (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    principal_id INTEGER NOT NULL REFERENCES kerberos_principals(id) ON DELETE CASCADE,
    key_type TEXT NOT NULL CHECK (key_type IN ('des-cbc-crc', 'des-cbc-md5', 'aes128-cts-hmac-sha1-96', 'aes256-cts-hmac-sha1-96', 'arcfour-hmac')),
    key_data TEXT NOT NULL,  -- Base64-encoded key
    salt TEXT,
    kvno INTEGER DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (principal_id) REFERENCES kerberos_principals(id)
);

-- Kerberos Tickets (TGT and service tickets)
CREATE TABLE IF NOT EXISTS kerberos_tickets (
    id TEXT PRIMARY KEY,  -- Ticket ID (UUID)
    client_principal TEXT NOT NULL,
    server_principal TEXT NOT NULL,
    realm TEXT NOT NULL,
    session_key TEXT NOT NULL,  -- Base64-encoded session key
    flags INTEGER DEFAULT 0,
    auth_time DATETIME NOT NULL,
    start_time DATETIME NOT NULL,
    end_time DATETIME NOT NULL,
    renew_till DATETIME,
    client_addresses TEXT,  -- JSON array of client IP addresses
    encrypted_ticket TEXT,  -- Base64-encoded encrypted ticket data
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Kerberos Realms (for cross-realm authentication)
CREATE TABLE IF NOT EXISTS kerberos_realms (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    realm TEXT UNIQUE NOT NULL,
    kdc_hostname TEXT NOT NULL,
    kdc_port INTEGER DEFAULT 88,
    admin_port INTEGER DEFAULT 749,
    is_local BOOLEAN DEFAULT FALSE,
    trust_type TEXT CHECK (trust_type IN ('none', 'one-way', 'two-way')),
    shared_secret TEXT,  -- For inter-realm trust
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Kerberos Policy (password policies and ticket lifetimes)
CREATE TABLE IF NOT EXISTS kerberos_policies (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    policy_name TEXT UNIQUE NOT NULL,
    min_password_length INTEGER DEFAULT 8,
    password_history INTEGER DEFAULT 5,
    max_ticket_lifetime INTEGER DEFAULT 86400,  -- 24 hours in seconds
    max_renewable_lifetime INTEGER DEFAULT 604800,  -- 7 days in seconds
    require_preauth BOOLEAN DEFAULT TRUE,
    lockout_threshold INTEGER DEFAULT 5,
    lockout_duration INTEGER DEFAULT 1800,  -- 30 minutes in seconds
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Kerberos Authentication Log
CREATE TABLE IF NOT EXISTS kerberos_auth_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    principal TEXT NOT NULL,
    realm TEXT NOT NULL,
    auth_type TEXT NOT NULL CHECK (auth_type IN ('AS-REQ', 'TGS-REQ', 'AP-REQ')),
    source_ip TEXT,
    success BOOLEAN NOT NULL,
    error_code INTEGER,
    error_message TEXT,
    service_principal TEXT,
    timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Kerberos Configuration
CREATE TABLE IF NOT EXISTS kerberos_config (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    config_key TEXT UNIQUE NOT NULL,
    config_value TEXT NOT NULL,
    description TEXT,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Indexes for performance
CREATE INDEX IF NOT EXISTS idx_kerberos_principals_realm ON kerberos_principals(realm);
CREATE INDEX IF NOT EXISTS idx_kerberos_principals_type ON kerberos_principals(type);
CREATE INDEX IF NOT EXISTS idx_kerberos_principals_enabled ON kerberos_principals(enabled);
CREATE INDEX IF NOT EXISTS idx_kerberos_tickets_client ON kerberos_tickets(client_principal);
CREATE INDEX IF NOT EXISTS idx_kerberos_tickets_server ON kerberos_tickets(server_principal);
CREATE INDEX IF NOT EXISTS idx_kerberos_tickets_end_time ON kerberos_tickets(end_time);
CREATE INDEX IF NOT EXISTS idx_kerberos_auth_log_principal ON kerberos_auth_log(principal);
CREATE INDEX IF NOT EXISTS idx_kerberos_auth_log_timestamp ON kerberos_auth_log(timestamp);
CREATE INDEX IF NOT EXISTS idx_kerberos_service_keys_principal ON kerberos_service_keys(principal_id);

-- Insert default realm configuration
INSERT OR IGNORE INTO kerberos_realms (realm, kdc_hostname, kdc_port, admin_port, is_local)
VALUES ('CASDC.LOCAL', 'localhost', 88, 749, TRUE);

-- Insert default Kerberos policy
INSERT OR IGNORE INTO kerberos_policies (policy_name, min_password_length, password_history, max_ticket_lifetime, max_renewable_lifetime)
VALUES ('default', 12, 5, 86400, 604800);
