-- Migration 003: Additional tables for FSMO, SSO, Remote Desktop services
-- Adds support for Active Directory FSMO roles, SSO authentication, and NoMachine remote desktop

-- FSMO Roles Tables
CREATE TABLE IF NOT EXISTS fsmo_roles (
    role TEXT PRIMARY KEY,
    node_id TEXT NOT NULL,
    node_name TEXT NOT NULL,
    since DATETIME NOT NULL,
    last_seen DATETIME NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active', 'standby', 'failed')),
    failover_node TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS fsmo_transfers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    role TEXT NOT NULL,
    from_node TEXT NOT NULL,
    to_node TEXT NOT NULL,
    reason TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'in_progress', 'completed', 'failed')),
    started_at DATETIME NOT NULL,
    completed_at DATETIME,
    error TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- SSO and Forward Authentication Tables
CREATE TABLE IF NOT EXISTS sso_sessions (
    session_id TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    username TEXT NOT NULL,
    email TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    last_activity DATETIME NOT NULL,
    expires_at DATETIME NOT NULL,
    ip_address TEXT NOT NULL,
    user_agent TEXT,
    active BOOLEAN DEFAULT TRUE,
    FOREIGN KEY (user_id) REFERENCES users(id)
);

CREATE TABLE IF NOT EXISTS sso_protected_services (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    domain TEXT NOT NULL,
    path TEXT,
    enabled BOOLEAN DEFAULT TRUE,
    require_auth BOOLEAN DEFAULT TRUE,
    logout_url TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS sso_service_groups (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    service_id TEXT NOT NULL REFERENCES sso_protected_services(id) ON DELETE CASCADE,
    group_name TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (service_id) REFERENCES sso_protected_services(id)
);

CREATE TABLE IF NOT EXISTS sso_header_injection (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    service_id TEXT NOT NULL REFERENCES sso_protected_services(id) ON DELETE CASCADE,
    header_name TEXT NOT NULL,
    header_value TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (service_id) REFERENCES sso_protected_services(id)
);

-- Remote Desktop (NoMachine) Tables
CREATE TABLE IF NOT EXISTS remote_desktop_sessions (
    session_id TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    client_ip TEXT NOT NULL,
    connected_at DATETIME NOT NULL,
    disconnected_at DATETIME,
    session_type TEXT NOT NULL CHECK (session_type IN ('desktop', 'application', 'shadow')),
    display_number INTEGER NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active', 'disconnected', 'reconnecting', 'terminated')),
    recording_path TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES users(id)
);

CREATE TABLE IF NOT EXISTS remote_desktop_policies (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    max_concurrent_sessions INTEGER DEFAULT 3,
    idle_timeout INTEGER DEFAULT 3600,
    max_session_duration INTEGER DEFAULT 28800,
    allow_recording BOOLEAN DEFAULT TRUE,
    allow_sharing BOOLEAN DEFAULT TRUE,
    allow_file_transfer BOOLEAN DEFAULT TRUE,
    allow_clipboard BOOLEAN DEFAULT TRUE,
    allow_printing BOOLEAN DEFAULT TRUE,
    allow_audio BOOLEAN DEFAULT TRUE,
    multi_monitor_enabled BOOLEAN DEFAULT TRUE,
    compression_level INTEGER DEFAULT 6 CHECK (compression_level BETWEEN 1 AND 9),
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES users(id)
);

CREATE TABLE IF NOT EXISTS remote_desktop_shared_sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES remote_desktop_sessions(session_id) ON DELETE CASCADE,
    shared_with_user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    shared_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (session_id) REFERENCES remote_desktop_sessions(session_id),
    FOREIGN KEY (shared_with_user_id) REFERENCES users(id)
);

-- Indexes for performance
CREATE INDEX IF NOT EXISTS idx_fsmo_roles_node ON fsmo_roles(node_id);
CREATE INDEX IF NOT EXISTS idx_fsmo_transfers_role ON fsmo_transfers(role);
CREATE INDEX IF NOT EXISTS idx_sso_sessions_user ON sso_sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sso_sessions_expires ON sso_sessions(expires_at);
CREATE INDEX IF NOT EXISTS idx_sso_sessions_active ON sso_sessions(active);
CREATE INDEX IF NOT EXISTS idx_sso_services_domain ON sso_protected_services(domain);
CREATE INDEX IF NOT EXISTS idx_remote_sessions_user ON remote_desktop_sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_remote_sessions_status ON remote_desktop_sessions(status);
CREATE INDEX IF NOT EXISTS idx_remote_policies_user ON remote_desktop_policies(user_id);
