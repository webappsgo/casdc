-- CASDC Complete Database Schema - Migration 002
-- This migration adds all remaining tables for complete functionality according to the spec

-- ============================================================================
-- ACTIVE DIRECTORY REPLACEMENT TABLES
-- ============================================================================

-- Computers table for domain-joined computers
CREATE TABLE IF NOT EXISTS computers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT UNIQUE NOT NULL,
    description TEXT,
    operating_system TEXT,
    os_version TEXT,
    dns_hostname TEXT,
    ip_address TEXT,
    mac_address TEXT,
    location TEXT,
    enabled BOOLEAN DEFAULT TRUE,
    trusted_for_delegation BOOLEAN DEFAULT FALSE,
    last_logon TIMESTAMP,
    password_last_set TIMESTAMP,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    ou_id INTEGER REFERENCES organizational_units(id)
);

-- Organizational Units for hierarchical structure
CREATE TABLE IF NOT EXISTS organizational_units (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    description TEXT,
    distinguished_name TEXT UNIQUE NOT NULL,
    parent_id INTEGER REFERENCES organizational_units(id),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    gpo_inheritance_blocked BOOLEAN DEFAULT FALSE,
    protect_from_deletion BOOLEAN DEFAULT FALSE
);

-- Group Policy Objects
CREATE TABLE IF NOT EXISTS group_policies (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT UNIQUE NOT NULL,
    display_name TEXT,
    description TEXT,
    version_directory INTEGER DEFAULT 1,
    version_sysvol INTEGER DEFAULT 1,
    enabled BOOLEAN DEFAULT TRUE,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    modified_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    settings_json TEXT
);

-- GPO Links to OUs
CREATE TABLE IF NOT EXISTS gpo_links (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    gpo_id INTEGER REFERENCES group_policies(id) ON DELETE CASCADE,
    ou_id INTEGER REFERENCES organizational_units(id) ON DELETE CASCADE,
    enabled BOOLEAN DEFAULT TRUE,
    enforced BOOLEAN DEFAULT FALSE,
    link_order INTEGER DEFAULT 0,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- WMI Filters for Group Policy
CREATE TABLE IF NOT EXISTS wmi_filters (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT UNIQUE NOT NULL,
    description TEXT,
    query TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================================
-- DNS SERVICES TABLES
-- ============================================================================

-- DNS Zones
CREATE TABLE IF NOT EXISTS dns_zones (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT UNIQUE NOT NULL,
    type TEXT DEFAULT 'forward' CHECK (type IN ('forward', 'reverse')),
    class TEXT DEFAULT 'IN',
    ttl INTEGER DEFAULT 86400,
    serial INTEGER DEFAULT 1,
    refresh INTEGER DEFAULT 3600,
    retry INTEGER DEFAULT 1800,
    expire INTEGER DEFAULT 604800,
    minimum INTEGER DEFAULT 86400,
    primary_ns TEXT,
    admin_email TEXT,
    enabled BOOLEAN DEFAULT TRUE,
    dnssec_enabled BOOLEAN DEFAULT FALSE,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- DNS Records
CREATE TABLE IF NOT EXISTS dns_records (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    zone_id INTEGER REFERENCES dns_zones(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('A', 'AAAA', 'CNAME', 'MX', 'TXT', 'SRV', 'PTR', 'NS', 'SOA')),
    value TEXT NOT NULL,
    ttl INTEGER DEFAULT 300,
    priority INTEGER DEFAULT 0,
    weight INTEGER DEFAULT 0,
    port INTEGER DEFAULT 0,
    enabled BOOLEAN DEFAULT TRUE,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================================
-- DHCP SERVICES TABLES
-- ============================================================================

-- DHCP Scopes
CREATE TABLE IF NOT EXISTS dhcp_scopes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    description TEXT,
    network TEXT NOT NULL,
    start_ip TEXT NOT NULL,
    end_ip TEXT NOT NULL,
    subnet_mask TEXT NOT NULL,
    default_gateway TEXT,
    dns_servers TEXT,
    domain_name TEXT,
    lease_time INTEGER DEFAULT 86400,
    enabled BOOLEAN DEFAULT TRUE,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- DHCP Reservations
CREATE TABLE IF NOT EXISTS dhcp_reservations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    scope_id INTEGER REFERENCES dhcp_scopes(id) ON DELETE CASCADE,
    hostname TEXT NOT NULL,
    mac_address TEXT UNIQUE NOT NULL,
    ip_address TEXT NOT NULL,
    description TEXT,
    enabled BOOLEAN DEFAULT TRUE,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- DHCP Options
CREATE TABLE IF NOT EXISTS dhcp_options (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    scope_id INTEGER REFERENCES dhcp_scopes(id) ON DELETE CASCADE,
    option_code INTEGER NOT NULL,
    option_name TEXT,
    option_value TEXT NOT NULL,
    enabled BOOLEAN DEFAULT TRUE
);

-- DHCP Leases
CREATE TABLE IF NOT EXISTS dhcp_leases (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    scope_id INTEGER REFERENCES dhcp_scopes(id),
    ip_address TEXT NOT NULL,
    mac_address TEXT NOT NULL,
    hostname TEXT,
    client_id TEXT,
    starts TIMESTAMP NOT NULL,
    ends TIMESTAMP NOT NULL,
    state TEXT DEFAULT 'active' CHECK (state IN ('active', 'expired', 'released')),
    binding_state TEXT DEFAULT 'active'
);

-- ============================================================================
-- MAIL SERVICES TABLES
-- ============================================================================

-- Mail Domains
CREATE TABLE IF NOT EXISTS mail_domains (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    domain TEXT UNIQUE NOT NULL,
    description TEXT,
    enabled BOOLEAN DEFAULT TRUE,
    relay_host TEXT,
    transport TEXT DEFAULT 'virtual',
    max_message_size INTEGER DEFAULT 25600000,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Mail Accounts
CREATE TABLE IF NOT EXISTS mail_accounts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
    email TEXT UNIQUE NOT NULL,
    domain_id INTEGER REFERENCES mail_domains(id),
    quota INTEGER DEFAULT 5368709120,
    enabled BOOLEAN DEFAULT TRUE,
    forwarding_address TEXT,
    vacation_enabled BOOLEAN DEFAULT FALSE,
    vacation_message TEXT,
    vacation_start DATE,
    vacation_end DATE,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Mail Aliases
CREATE TABLE IF NOT EXISTS mail_aliases (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    alias TEXT NOT NULL,
    domain_id INTEGER REFERENCES mail_domains(id),
    destination TEXT NOT NULL,
    enabled BOOLEAN DEFAULT TRUE,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================================
-- WEB SERVICES TABLES
-- ============================================================================

-- SSL Certificates
CREATE TABLE IF NOT EXISTS ssl_certificates (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    domain TEXT NOT NULL,
    type TEXT DEFAULT 'letsencrypt' CHECK (type IN ('letsencrypt', 'internal', 'uploaded')),
    certificate TEXT,
    private_key TEXT,
    ca_chain TEXT,
    issued_at TIMESTAMP,
    expires_at TIMESTAMP,
    auto_renew BOOLEAN DEFAULT TRUE,
    provider TEXT,
    provider_config TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    last_renewed TIMESTAMP
);

-- Web Sites
CREATE TABLE IF NOT EXISTS web_sites (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    domain TEXT UNIQUE NOT NULL,
    document_root TEXT NOT NULL,
    enabled BOOLEAN DEFAULT TRUE,
    ssl_enabled BOOLEAN DEFAULT TRUE,
    ssl_cert_id INTEGER REFERENCES ssl_certificates(id),
    php_enabled BOOLEAN DEFAULT FALSE,
    php_version TEXT DEFAULT '8.1',
    redirect_to TEXT,
    auth_required BOOLEAN DEFAULT FALSE,
    auth_realm TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    server_admin TEXT,
    custom_config TEXT
);

-- ============================================================================
-- FILE SHARING SERVICES TABLES
-- ============================================================================

-- File Shares
CREATE TABLE IF NOT EXISTS file_shares (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT UNIQUE NOT NULL,
    path TEXT NOT NULL,
    description TEXT,
    enabled BOOLEAN DEFAULT TRUE,
    read_only BOOLEAN DEFAULT FALSE,
    browseable BOOLEAN DEFAULT TRUE,
    guest_ok BOOLEAN DEFAULT FALSE,
    create_mask TEXT DEFAULT '0755',
    directory_mask TEXT DEFAULT '0755',
    force_user TEXT,
    force_group TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Share Permissions
CREATE TABLE IF NOT EXISTS share_permissions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    share_id INTEGER REFERENCES file_shares(id) ON DELETE CASCADE,
    principal_type TEXT CHECK (principal_type IN ('user', 'group')),
    principal_id INTEGER,
    permission TEXT CHECK (permission IN ('read', 'write', 'full')),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================================
-- VPN SERVICES TABLES
-- ============================================================================

-- VPN Servers
CREATE TABLE IF NOT EXISTS vpn_servers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT UNIQUE NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('openvpn', 'wireguard', 'ipsec')),
    enabled BOOLEAN DEFAULT TRUE,
    listen_port INTEGER,
    protocol TEXT DEFAULT 'udp',
    network TEXT,
    dns_servers TEXT,
    routes TEXT,
    config TEXT,
    ca_cert TEXT,
    server_cert TEXT,
    server_key TEXT,
    dh_params TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- VPN Clients
CREATE TABLE IF NOT EXISTS vpn_clients (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id INTEGER REFERENCES vpn_servers(id) ON DELETE CASCADE,
    user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    enabled BOOLEAN DEFAULT TRUE,
    config TEXT,
    certificate TEXT,
    private_key TEXT,
    public_key TEXT,
    assigned_ip TEXT,
    last_connected TIMESTAMP,
    bytes_sent INTEGER DEFAULT 0,
    bytes_received INTEGER DEFAULT 0,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================================
-- SECURITY TABLES
-- ============================================================================

-- User Tokens (API, reset, activation)
CREATE TABLE IF NOT EXISTS user_tokens (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL,
    token_type TEXT NOT NULL CHECK (token_type IN ('api', 'reset', 'activation')),
    expires_at TIMESTAMP NOT NULL,
    used_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    description TEXT
);

-- MFA Settings
CREATE TABLE IF NOT EXISTS mfa_settings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
    method TEXT NOT NULL CHECK (method IN ('totp', 'sms', 'email')),
    secret TEXT NOT NULL,
    backup_codes TEXT,
    enabled BOOLEAN DEFAULT FALSE,
    verified_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Security Events
CREATE TABLE IF NOT EXISTS security_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    event_type TEXT NOT NULL,
    severity TEXT CHECK (severity IN ('low', 'medium', 'high', 'critical')),
    source_ip TEXT,
    user_id INTEGER REFERENCES users(id),
    description TEXT NOT NULL,
    details TEXT,
    resolved BOOLEAN DEFAULT FALSE,
    resolved_by INTEGER REFERENCES users(id),
    resolved_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Threat Intelligence
CREATE TABLE IF NOT EXISTS threat_intelligence (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ioc_type TEXT CHECK (ioc_type IN ('ip', 'domain', 'hash', 'url')),
    ioc_value TEXT NOT NULL,
    threat_type TEXT,
    confidence INTEGER CHECK (confidence BETWEEN 0 AND 100),
    source TEXT NOT NULL,
    first_seen TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    last_seen TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    active BOOLEAN DEFAULT TRUE
);

-- Quarantine
CREATE TABLE IF NOT EXISTS quarantine (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    file_path TEXT NOT NULL,
    original_path TEXT NOT NULL,
    threat_type TEXT,
    detection_engine TEXT,
    quarantined_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    quarantined_by TEXT,
    released BOOLEAN DEFAULT FALSE,
    released_at TIMESTAMP,
    released_by INTEGER REFERENCES users(id)
);

-- ============================================================================
-- BACKUP TABLES
-- ============================================================================

-- Backup Jobs
CREATE TABLE IF NOT EXISTS backup_jobs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT UNIQUE NOT NULL,
    description TEXT,
    backup_type TEXT CHECK (backup_type IN ('full', 'incremental', 'differential')),
    source_paths TEXT,
    destination TEXT NOT NULL,
    schedule TEXT,
    retention_days INTEGER DEFAULT 30,
    compression BOOLEAN DEFAULT TRUE,
    encryption BOOLEAN DEFAULT TRUE,
    encryption_key_id TEXT,
    enabled BOOLEAN DEFAULT TRUE,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    last_run TIMESTAMP,
    next_run TIMESTAMP
);

-- Backup History
CREATE TABLE IF NOT EXISTS backup_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id INTEGER REFERENCES backup_jobs(id) ON DELETE CASCADE,
    started_at TIMESTAMP NOT NULL,
    completed_at TIMESTAMP,
    status TEXT CHECK (status IN ('running', 'completed', 'failed', 'cancelled')),
    files_backed_up INTEGER DEFAULT 0,
    bytes_backed_up INTEGER DEFAULT 0,
    duration_seconds INTEGER,
    error_message TEXT,
    backup_path TEXT
);

-- ============================================================================
-- MONITORING TABLES
-- ============================================================================

-- System Logs
CREATE TABLE IF NOT EXISTS system_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    level TEXT CHECK (level IN ('debug', 'info', 'warn', 'error', 'fatal')),
    component TEXT NOT NULL,
    message TEXT NOT NULL,
    details TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Performance Metrics
CREATE TABLE IF NOT EXISTS performance_metrics (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    metric_name TEXT NOT NULL,
    metric_value REAL NOT NULL,
    tags TEXT,
    collected_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Health Checks
CREATE TABLE IF NOT EXISTS health_checks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    service_name TEXT NOT NULL,
    status TEXT CHECK (status IN ('healthy', 'degraded', 'unhealthy')),
    response_time_ms INTEGER,
    error_message TEXT,
    checked_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================================
-- CLUSTERING TABLES
-- ============================================================================

-- Cluster Nodes
CREATE TABLE IF NOT EXISTS cluster_nodes (
    id TEXT PRIMARY KEY,
    hostname TEXT NOT NULL,
    ip_address TEXT NOT NULL,
    role TEXT CHECK (role IN ('primary', 'secondary', 'witness')),
    status TEXT CHECK (status IN ('online', 'offline', 'maintenance')),
    version TEXT,
    last_heartbeat TIMESTAMP,
    joined_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    capabilities TEXT,
    load_average REAL,
    memory_usage_percent REAL,
    disk_usage_percent REAL
);

-- Sync Events
CREATE TABLE IF NOT EXISTS sync_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    source_node_id TEXT REFERENCES cluster_nodes(id),
    target_node_id TEXT REFERENCES cluster_nodes(id),
    sync_type TEXT NOT NULL,
    table_name TEXT,
    record_id TEXT,
    operation TEXT CHECK (operation IN ('insert', 'update', 'delete')),
    status TEXT CHECK (status IN ('pending', 'completed', 'failed')),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMP,
    error_message TEXT
);

-- ============================================================================
-- SUPPORT SYSTEM TABLES
-- ============================================================================

-- Support Tickets
CREATE TABLE IF NOT EXISTS support_tickets (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ticket_number TEXT UNIQUE NOT NULL,
    user_id INTEGER REFERENCES users(id),
    subject TEXT NOT NULL,
    description TEXT NOT NULL,
    priority TEXT DEFAULT 'medium' CHECK (priority IN ('low', 'medium', 'high', 'urgent')),
    status TEXT DEFAULT 'open' CHECK (status IN ('open', 'in_progress', 'waiting', 'resolved', 'closed')),
    category TEXT,
    assigned_to INTEGER REFERENCES users(id),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    resolved_at TIMESTAMP,
    resolution TEXT
);

-- Ticket Messages
CREATE TABLE IF NOT EXISTS ticket_messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ticket_id INTEGER REFERENCES support_tickets(id) ON DELETE CASCADE,
    user_id INTEGER REFERENCES users(id),
    message TEXT NOT NULL,
    message_type TEXT DEFAULT 'reply' CHECK (message_type IN ('reply', 'note', 'status_change')),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    attachments TEXT
);

-- ============================================================================
-- EXCHANGE ENTERPRISE TABLES
-- ============================================================================

-- Mobile Devices (ActiveSync)
CREATE TABLE IF NOT EXISTS mobile_devices (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
    device_id TEXT UNIQUE NOT NULL,
    device_type TEXT,
    device_model TEXT,
    device_os TEXT,
    device_os_version TEXT,
    device_imei TEXT,
    device_phone_number TEXT,
    friendly_name TEXT,
    first_sync_time TIMESTAMP,
    last_sync_attempt TIMESTAMP,
    last_successful_sync TIMESTAMP,
    sync_state TEXT,
    device_access_state TEXT DEFAULT 'allowed' CHECK (device_access_state IN ('allowed', 'blocked', 'quarantined')),
    device_access_state_reason TEXT,
    policy_id INTEGER REFERENCES device_policies(id),
    wipe_requested BOOLEAN DEFAULT FALSE,
    wipe_requested_at TIMESTAMP,
    wipe_acknowledged BOOLEAN DEFAULT FALSE,
    wipe_acknowledged_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    heartbeat_interval INTEGER DEFAULT 3600,
    protocol_version TEXT,
    user_agent TEXT
);

-- Device Policies (MDM)
CREATE TABLE IF NOT EXISTS device_policies (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT UNIQUE NOT NULL,
    description TEXT,
    allow_simple_password BOOLEAN DEFAULT FALSE,
    alphanumeric_password_required BOOLEAN DEFAULT TRUE,
    password_recovery_enabled BOOLEAN DEFAULT TRUE,
    device_encryption_enabled BOOLEAN DEFAULT TRUE,
    attachments_enabled BOOLEAN DEFAULT TRUE,
    min_password_length INTEGER DEFAULT 6,
    max_password_failed_attempts INTEGER DEFAULT 10,
    max_inactivity_time_lock INTEGER DEFAULT 900,
    max_attachment_size INTEGER DEFAULT 10485760,
    allow_storagecard BOOLEAN DEFAULT TRUE,
    allow_camera BOOLEAN DEFAULT TRUE,
    require_device_encryption BOOLEAN DEFAULT FALSE,
    allow_unsigned_applications BOOLEAN DEFAULT FALSE,
    allow_unsigned_installation_packages BOOLEAN DEFAULT FALSE,
    min_password_complex_characters INTEGER DEFAULT 0,
    max_calendar_age_filter INTEGER DEFAULT 0,
    max_email_age_filter INTEGER DEFAULT 0,
    max_email_body_truncation_size INTEGER DEFAULT 0,
    max_email_html_body_truncation_size INTEGER DEFAULT 0,
    require_signed_smime_messages BOOLEAN DEFAULT FALSE,
    require_encrypted_smime_messages BOOLEAN DEFAULT FALSE,
    require_signed_smime_algorithm TEXT,
    require_encryption_smime_algorithm TEXT,
    allow_smime_encryption_algorithm_negotiation BOOLEAN DEFAULT TRUE,
    allow_smime_soft_certs BOOLEAN DEFAULT TRUE,
    allow_browser BOOLEAN DEFAULT TRUE,
    allow_consumer_email BOOLEAN DEFAULT TRUE,
    allow_remote_desktop BOOLEAN DEFAULT TRUE,
    allow_internet_sharing BOOLEAN DEFAULT TRUE,
    allow_irda BOOLEAN DEFAULT TRUE,
    allow_wifi BOOLEAN DEFAULT TRUE,
    allow_text_messaging BOOLEAN DEFAULT TRUE,
    allow_pop_imap_email BOOLEAN DEFAULT TRUE,
    allow_bluetooth BOOLEAN DEFAULT TRUE,
    require_manual_sync_when_roaming BOOLEAN DEFAULT FALSE,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    is_default BOOLEAN DEFAULT FALSE
);

-- ActiveSync Settings
CREATE TABLE IF NOT EXISTS activesync_settings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
    enabled BOOLEAN DEFAULT TRUE,
    max_concurrent_connections INTEGER DEFAULT 10,
    max_request_timeout INTEGER DEFAULT 300,
    heartbeat_interval INTEGER DEFAULT 3600,
    max_folder_hierarchy_depth INTEGER DEFAULT 10,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- EWS Settings
CREATE TABLE IF NOT EXISTS ews_settings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    enabled BOOLEAN DEFAULT TRUE,
    max_concurrent_connections INTEGER DEFAULT 100,
    max_request_size INTEGER DEFAULT 10485760,
    throttling_enabled BOOLEAN DEFAULT TRUE,
    throttling_max_requests_per_minute INTEGER DEFAULT 60,
    impersonation_enabled BOOLEAN DEFAULT FALSE,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Autodiscover Settings
CREATE TABLE IF NOT EXISTS autodiscover_settings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    enabled BOOLEAN DEFAULT TRUE,
    internal_url TEXT,
    external_url TEXT,
    redirect_enabled BOOLEAN DEFAULT TRUE,
    client_access_server TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Public Folders
CREATE TABLE IF NOT EXISTS public_folders (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    path TEXT UNIQUE NOT NULL,
    description TEXT,
    folder_type TEXT DEFAULT 'mail' CHECK (folder_type IN ('mail', 'calendar', 'contacts', 'tasks', 'notes')),
    enabled BOOLEAN DEFAULT TRUE,
    mail_enabled BOOLEAN DEFAULT FALSE,
    email_address TEXT,
    quota_storage_warning INTEGER DEFAULT 1900000000,
    quota_storage_prohibit_send INTEGER DEFAULT 2000000000,
    quota_storage_prohibit_send_receive INTEGER DEFAULT 2300000000,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    created_by INTEGER REFERENCES users(id),
    parent_folder_id INTEGER REFERENCES public_folders(id)
);

-- Public Folder Permissions
CREATE TABLE IF NOT EXISTS public_folder_permissions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    folder_id INTEGER REFERENCES public_folders(id) ON DELETE CASCADE,
    principal_type TEXT CHECK (principal_type IN ('user', 'group', 'anonymous', 'default')),
    principal_id INTEGER,
    permission_level TEXT CHECK (permission_level IN ('none', 'readonly', 'readwrite', 'owner')) DEFAULT 'readonly',
    can_create_items BOOLEAN DEFAULT FALSE,
    can_read_items BOOLEAN DEFAULT TRUE,
    can_edit_own_items BOOLEAN DEFAULT FALSE,
    can_edit_all_items BOOLEAN DEFAULT FALSE,
    can_delete_own_items BOOLEAN DEFAULT FALSE,
    can_delete_all_items BOOLEAN DEFAULT FALSE,
    can_create_subfolders BOOLEAN DEFAULT FALSE,
    is_folder_owner BOOLEAN DEFAULT FALSE,
    is_folder_contact BOOLEAN DEFAULT FALSE,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Free/Busy Settings
CREATE TABLE IF NOT EXISTS freebusy_settings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
    enabled BOOLEAN DEFAULT TRUE,
    start_time TIME DEFAULT '08:00:00',
    end_time TIME DEFAULT '17:00:00',
    timezone TEXT DEFAULT 'UTC',
    publish_months_ahead INTEGER DEFAULT 2,
    publish_months_behind INTEGER DEFAULT 1,
    external_audience TEXT DEFAULT 'none' CHECK (external_audience IN ('none', 'known', 'all')),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================================
-- INDEXES FOR PERFORMANCE
-- ============================================================================

-- Active Directory indexes
CREATE INDEX IF NOT EXISTS idx_computers_name ON computers(name);
CREATE INDEX IF NOT EXISTS idx_computers_enabled ON computers(enabled);
CREATE INDEX IF NOT EXISTS idx_computers_ou_id ON computers(ou_id);
CREATE INDEX IF NOT EXISTS idx_organizational_units_parent_id ON organizational_units(parent_id);
CREATE INDEX IF NOT EXISTS idx_gpo_links_gpo_id ON gpo_links(gpo_id);
CREATE INDEX IF NOT EXISTS idx_gpo_links_ou_id ON gpo_links(ou_id);

-- DNS indexes
CREATE INDEX IF NOT EXISTS idx_dns_zones_name ON dns_zones(name);
CREATE INDEX IF NOT EXISTS idx_dns_records_zone_id ON dns_records(zone_id);
CREATE INDEX IF NOT EXISTS idx_dns_records_name ON dns_records(name);
CREATE INDEX IF NOT EXISTS idx_dns_records_type ON dns_records(type);

-- DHCP indexes
CREATE INDEX IF NOT EXISTS idx_dhcp_reservations_mac ON dhcp_reservations(mac_address);
CREATE INDEX IF NOT EXISTS idx_dhcp_leases_ip ON dhcp_leases(ip_address);
CREATE INDEX IF NOT EXISTS idx_dhcp_leases_mac ON dhcp_leases(mac_address);
CREATE INDEX IF NOT EXISTS idx_dhcp_leases_ends ON dhcp_leases(ends);

-- Mail indexes
CREATE INDEX IF NOT EXISTS idx_mail_accounts_email ON mail_accounts(email);
CREATE INDEX IF NOT EXISTS idx_mail_accounts_user_id ON mail_accounts(user_id);
CREATE INDEX IF NOT EXISTS idx_mail_aliases_alias ON mail_aliases(alias);

-- Security indexes
CREATE INDEX IF NOT EXISTS idx_security_events_created ON security_events(created_at);
CREATE INDEX IF NOT EXISTS idx_security_events_severity ON security_events(severity);
CREATE INDEX IF NOT EXISTS idx_threat_intelligence_ioc_value ON threat_intelligence(ioc_value);
CREATE INDEX IF NOT EXISTS idx_user_tokens_user_id ON user_tokens(user_id);
CREATE INDEX IF NOT EXISTS idx_user_tokens_expires ON user_tokens(expires_at);

-- Backup indexes
CREATE INDEX IF NOT EXISTS idx_backup_history_job_id ON backup_history(job_id);
CREATE INDEX IF NOT EXISTS idx_backup_history_started ON backup_history(started_at);

-- Monitoring indexes
CREATE INDEX IF NOT EXISTS idx_system_logs_level ON system_logs(level);
CREATE INDEX IF NOT EXISTS idx_system_logs_created ON system_logs(created_at);
CREATE INDEX IF NOT EXISTS idx_performance_metrics_name ON performance_metrics(metric_name);
CREATE INDEX IF NOT EXISTS idx_performance_metrics_collected ON performance_metrics(collected_at);

-- Exchange Enterprise indexes
CREATE INDEX IF NOT EXISTS idx_mobile_devices_user_id ON mobile_devices(user_id);
CREATE INDEX IF NOT EXISTS idx_mobile_devices_device_id ON mobile_devices(device_id);
CREATE INDEX IF NOT EXISTS idx_public_folders_parent_id ON public_folders(parent_folder_id);
CREATE INDEX IF NOT EXISTS idx_public_folder_permissions_folder_id ON public_folder_permissions(folder_id);

-- Support indexes
CREATE INDEX IF NOT EXISTS idx_support_tickets_user_id ON support_tickets(user_id);
CREATE INDEX IF NOT EXISTS idx_support_tickets_status ON support_tickets(status);
CREATE INDEX IF NOT EXISTS idx_support_tickets_created ON support_tickets(created_at);
CREATE INDEX IF NOT EXISTS idx_ticket_messages_ticket_id ON ticket_messages(ticket_id);

-- Clustering indexes
CREATE INDEX IF NOT EXISTS idx_cluster_nodes_status ON cluster_nodes(status);
CREATE INDEX IF NOT EXISTS idx_sync_events_source_node ON sync_events(source_node_id);
CREATE INDEX IF NOT EXISTS idx_sync_events_target_node ON sync_events(target_node_id);
CREATE INDEX IF NOT EXISTS idx_sync_events_status ON sync_events(status);