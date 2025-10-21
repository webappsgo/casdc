// Package configgen provides configuration file generation from database-driven templates
// All service configurations are generated dynamically using Go templates with custom functions
package configgen

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"text/template"
	"time"

	"github.com/casapps/casdc/internal/config"
	"github.com/casapps/casdc/internal/database"
	"github.com/casapps/casdc/pkg/logger"
)

// Service represents a configuration generation service
// This service reads configuration data from the database and generates
// service-specific configuration files using Go templates
type Service struct {
	db       *database.DB
	config   *config.Config
	logger   *logger.Logger
	funcMap  template.FuncMap
	templates map[string]*template.Template
}

// TemplateVars holds all variables available for template substitution
// These variables are populated from the database and configuration
type TemplateVars struct {
	ProjectName   string // "casdc" (fixed, unchangeable)
	Domain        string // User's primary domain (e.g., example.com)
	ServerDomain  string // Server FQDN (e.g., casdc.example.com)
	ServerAddress string // Server IP or hostname
	BindConfDir   string // Distribution-specific BIND directory
	Organization  string // Organization name for certificates
	AdminEmail    string // Primary administrator email address
	Timezone      string // System timezone

	// Service-specific variables (populated per service)
	CustomVars map[string]interface{}
}

// NewService creates a new configuration generation service
// Initializes custom template functions for secure template processing
func NewService(db *database.DB, cfg *config.Config, log *logger.Logger) *Service {
	s := &Service{
		db:        db,
		config:    cfg,
		logger:    log,
		templates: make(map[string]*template.Template),
	}

	// Initialize custom template functions
	s.funcMap = template.FuncMap{
		// Encoding functions
		"base64encode": s.base64Encode,
		"base64decode": s.base64Decode,

		// Cryptographic functions
		"encrypt": s.encrypt,
		"decrypt": s.decrypt,
		"hash":    s.hashSHA256,

		// Security functions
		"sanitize": s.sanitize,
		"validate": s.validate,

		// Formatting functions
		"formattime": s.formatTime,
		"urlencode":  s.urlEncode,
		"urldecode":  s.urlDecode,
		"htmlescape": s.htmlEscape,

		// String functions
		"lower":      strings.ToLower,
		"upper":      strings.ToUpper,
		"trim":       strings.TrimSpace,
		"replace":    strings.ReplaceAll,
		"contains":   strings.Contains,
		"hasPrefix":  strings.HasPrefix,
		"hasSuffix":  strings.HasSuffix,
		"join":       strings.Join,
		"split":      strings.Split,

		// Network functions
		"isIPv4": s.isIPv4,
		"isIPv6": s.isIPv6,
		"isDomain": s.isDomain,

		// Conditional functions
		"default": s.defaultValue,
	}

	return s
}

// GetTemplateVars retrieves template variables from database and configuration
// Returns a populated TemplateVars struct ready for template rendering
func (s *Service) GetTemplateVars() (*TemplateVars, error) {
	vars := &TemplateVars{
		ProjectName:   "casdc", // Fixed, unchangeable
		CustomVars:    make(map[string]interface{}),
	}

	// Get configuration values from database
	domain, err := s.getConfigValue("domain")
	if err == nil && domain != "" {
		vars.Domain = domain
	} else {
		vars.Domain = s.config.Domain
	}

	serverDomain, err := s.getConfigValue("server_domain")
	if err == nil && serverDomain != "" {
		vars.ServerDomain = serverDomain
	} else {
		vars.ServerDomain = fmt.Sprintf("casdc.%s", vars.Domain)
	}

	vars.ServerAddress = s.config.ServerAddress

	// Detect BIND configuration directory based on OS
	vars.BindConfDir = s.detectBindConfDir()

	organization, err := s.getConfigValue("organization")
	if err == nil && organization != "" {
		vars.Organization = organization
	}

	adminEmail, err := s.getConfigValue("admin_email")
	if err == nil && adminEmail != "" {
		vars.AdminEmail = adminEmail
	}

	timezone, err := s.getConfigValue("timezone")
	if err == nil && timezone != "" {
		vars.Timezone = timezone
	} else {
		vars.Timezone = "UTC"
	}

	return vars, nil
}

// getConfigValue retrieves a configuration value from the database
func (s *Service) getConfigValue(key string) (string, error) {
	var value string
	err := s.db.QueryRow(
		"SELECT value FROM casdc_config WHERE key = ?", key,
	).Scan(&value)
	return value, err
}

// detectBindConfDir detects the BIND configuration directory for the current OS
// Returns appropriate path based on distribution detection
func (s *Service) detectBindConfDir() string {
	// Distribution-specific paths
	// Debian/Ubuntu: /etc/bind
	// RHEL/CentOS: /etc/named
	// FreeBSD: /usr/local/etc/named

	// For now, return default based on common distributions
	// In production, detect actual OS and distribution
	return "/etc/bind"
}

// GenerateConfig generates a configuration file from a template
// template: template content with {variable} placeholders
// serviceName: name of the service for logging
// Returns the generated configuration as a string
func (s *Service) GenerateConfig(templateContent string, serviceName string, customVars map[string]interface{}) (string, error) {
	s.logger.Debug("Generating configuration for %s", serviceName)

	// Get template variables
	vars, err := s.GetTemplateVars()
	if err != nil {
		return "", fmt.Errorf("failed to get template variables: %w", err)
	}

	// Merge custom variables
	for k, v := range customVars {
		vars.CustomVars[k] = v
	}

	// Parse template
	tmpl, err := template.New(serviceName).Funcs(s.funcMap).Parse(templateContent)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	// Execute template
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.String(), nil
}

// ============================================================================
// TEMPLATE FUNCTIONS
// ============================================================================

// base64Encode encodes a string to base64
func (s *Service) base64Encode(str string) string {
	return base64.StdEncoding.EncodeToString([]byte(str))
}

// base64Decode decodes a base64 string
func (s *Service) base64Decode(str string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(str)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

// encrypt encrypts a string using AES-256 (placeholder for actual implementation)
// In production, implement proper AES-256-GCM encryption with key management
func (s *Service) encrypt(str string) (string, error) {
	// Placeholder: In production, implement AES-256-GCM encryption
	s.logger.Warn("encrypt() is a placeholder, implement AES-256-GCM encryption")
	return s.base64Encode(str), nil
}

// decrypt decrypts an encrypted string (placeholder for actual implementation)
// In production, implement proper AES-256-GCM decryption with key management
func (s *Service) decrypt(str string) (string, error) {
	// Placeholder: In production, implement AES-256-GCM decryption
	s.logger.Warn("decrypt() is a placeholder, implement AES-256-GCM decryption")
	return s.base64Decode(str)
}

// hashSHA256 generates SHA-256 hash of a string
func (s *Service) hashSHA256(str string) string {
	hash := sha256.Sum256([]byte(str))
	return hex.EncodeToString(hash[:])
}

// sanitize sanitizes input string for security
// Removes potentially dangerous characters and patterns
func (s *Service) sanitize(str string) string {
	// Remove null bytes
	str = strings.ReplaceAll(str, "\x00", "")

	// Remove control characters except newline, carriage return, and tab
	var sanitized strings.Builder
	for _, r := range str {
		if r >= 32 || r == '\n' || r == '\r' || r == '\t' {
			sanitized.WriteRune(r)
		}
	}

	return sanitized.String()
}

// validate validates input based on type
// Supported types: email, domain, ip, ipv4, ipv6, url
func (s *Service) validate(str string, validationType string) bool {
	switch validationType {
	case "email":
		return s.isValidEmail(str)
	case "domain":
		return s.isDomain(str)
	case "ip", "ipv4":
		return s.isIPv4(str)
	case "ipv6":
		return s.isIPv6(str)
	case "url":
		return s.isValidURL(str)
	default:
		s.logger.Warn("Unknown validation type: %s", validationType)
		return false
	}
}

// formatTime formats a time value with a given layout
func (s *Service) formatTime(layout string, t time.Time) string {
	return t.Format(layout)
}

// urlEncode URL-encodes a string
func (s *Service) urlEncode(str string) string {
	return url.QueryEscape(str)
}

// urlDecode URL-decodes a string
func (s *Service) urlDecode(str string) (string, error) {
	return url.QueryUnescape(str)
}

// htmlEscape HTML-escapes a string
func (s *Service) htmlEscape(str string) string {
	return html.EscapeString(str)
}

// isIPv4 checks if a string is a valid IPv4 address
func (s *Service) isIPv4(str string) bool {
	pattern := `^((25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.){3}(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)$`
	matched, _ := regexp.MatchString(pattern, str)
	return matched
}

// isIPv6 checks if a string is a valid IPv6 address
func (s *Service) isIPv6(str string) bool {
	pattern := `^(([0-9a-fA-F]{1,4}:){7,7}[0-9a-fA-F]{1,4}|([0-9a-fA-F]{1,4}:){1,7}:|([0-9a-fA-F]{1,4}:){1,6}:[0-9a-fA-F]{1,4}|([0-9a-fA-F]{1,4}:){1,5}(:[0-9a-fA-F]{1,4}){1,2}|([0-9a-fA-F]{1,4}:){1,4}(:[0-9a-fA-F]{1,4}){1,3}|([0-9a-fA-F]{1,4}:){1,3}(:[0-9a-fA-F]{1,4}){1,4}|([0-9a-fA-F]{1,4}:){1,2}(:[0-9a-fA-F]{1,4}){1,5}|[0-9a-fA-F]{1,4}:((:[0-9a-fA-F]{1,4}){1,6})|:((:[0-9a-fA-F]{1,4}){1,7}|:)|fe80:(:[0-9a-fA-F]{0,4}){0,4}%[0-9a-zA-Z]{1,}|::(ffff(:0{1,4}){0,1}:){0,1}((25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9])\.){3,3}(25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9])|([0-9a-fA-F]{1,4}:){1,4}:((25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9])\.){3,3}(25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9]))$`
	matched, _ := regexp.MatchString(pattern, str)
	return matched
}

// isDomain checks if a string is a valid domain name
func (s *Service) isDomain(str string) bool {
	pattern := `^([a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$`
	matched, _ := regexp.MatchString(pattern, str)
	return matched
}

// isValidEmail checks if a string is a valid email address
func (s *Service) isValidEmail(str string) bool {
	pattern := `^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`
	matched, _ := regexp.MatchString(pattern, str)
	return matched
}

// isValidURL checks if a string is a valid URL
func (s *Service) isValidURL(str string) bool {
	_, err := url.ParseRequestURI(str)
	return err == nil
}

// defaultValue returns a default value if the input is empty
func (s *Service) defaultValue(defaultVal interface{}, value interface{}) interface{} {
	if value == nil || value == "" {
		return defaultVal
	}
	return value
}

// ============================================================================
// SERVICE-SPECIFIC CONFIGURATION GENERATORS
// ============================================================================

// GenerateNginxConfig generates nginx configuration
func (s *Service) GenerateNginxConfig() (string, error) {
	template := `# CASDC nginx configuration - Generated automatically
# Do not edit manually - changes will be overwritten

user www-data;
worker_processes auto;
pid /run/nginx.pid;
error_log /var/log/{{.ProjectName}}/nginx-error.log warn;

events {
    worker_connections 2048;
    use epoll;
    multi_accept on;
}

http {
    # Basic settings
    sendfile on;
    tcp_nopush on;
    tcp_nodelay on;
    keepalive_timeout 65;
    types_hash_max_size 2048;
    server_tokens off;
    client_max_body_size 100M;

    # MIME types
    include /etc/nginx/mime.types;
    default_type application/octet-stream;

    # Logging
    access_log /var/log/{{.ProjectName}}/nginx-access.log;

    # Gzip compression
    gzip on;
    gzip_vary on;
    gzip_proxied any;
    gzip_comp_level 6;
    gzip_types text/plain text/css text/xml text/javascript
               application/json application/javascript application/xml+rss
               application/rss+xml font/truetype font/opentype
               application/vnd.ms-fontobject image/svg+xml;

    # SSL/TLS settings
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers 'ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384';
    ssl_prefer_server_ciphers off;
    ssl_session_cache shared:SSL:50m;
    ssl_session_timeout 1d;
    ssl_session_tickets off;

    # Security headers
    add_header X-Frame-Options "DENY" always;
    add_header X-Content-Type-Options "nosniff" always;
    add_header X-XSS-Protection "1; mode=block" always;
    add_header Referrer-Policy "strict-origin-when-cross-origin" always;

    # CASDC main server
    server {
        listen 80 default_server;
        listen [::]:80 default_server;
        server_name {{.ServerDomain}} {{.ServerAddress}};

        # Redirect HTTP to HTTPS
        return 301 https://$host$request_uri;
    }

    server {
        listen 443 ssl http2 default_server;
        listen [::]:443 ssl http2 default_server;
        server_name {{.ServerDomain}} {{.ServerAddress}};

        # SSL certificate (will be updated by certificate service)
        ssl_certificate /etc/{{.ProjectName}}/certs/{{.ServerDomain}}/fullchain.pem;
        ssl_certificate_key /etc/{{.ProjectName}}/certs/{{.ServerDomain}}/privkey.pem;

        # HSTS header
        add_header Strict-Transport-Security "max-age=31536000; includeSubDomains; preload" always;

        # Document root
        root /var/www/default;
        index index.html;

        # CASDC web interface
        location / {
            try_files $uri $uri/ =404;
        }

        # API proxy to CASDC backend
        location /api/ {
            proxy_pass http://127.0.0.1:8080;
            proxy_http_version 1.1;
            proxy_set_header Upgrade $http_upgrade;
            proxy_set_header Connection 'upgrade';
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;
            proxy_cache_bypass $http_upgrade;
        }

        # Webmail (SnappyMail)
        location /webmail/ {
            alias /var/www/default/webmail/;
            index index.php;

            location ~ \.php$ {
                include fastcgi_params;
                fastcgi_pass unix:/var/run/php/php-fpm.sock;
                fastcgi_param SCRIPT_FILENAME $request_filename;
            }
        }
    }

    # Include additional site configurations
    include /etc/nginx/sites-enabled/*;
}
`

	return s.GenerateConfig(template, "nginx", nil)
}

// GeneratePostfixConfig generates Postfix main.cf configuration
func (s *Service) GeneratePostfixConfig() (string, error) {
	template := `# CASDC Postfix configuration - Generated automatically
# Do not edit manually - changes will be overwritten

# Basic settings
myhostname = {{.ServerDomain}}
mydomain = {{.Domain}}
myorigin = $mydomain
mydestination = $myhostname, localhost.$mydomain, localhost
mynetworks = 127.0.0.0/8, [::1]/128

# Mail queue settings
queue_directory = /var/spool/postfix
mail_owner = postfix
default_privs = nobody

# Virtual domains and mailboxes
virtual_mailbox_domains = proxy:pgsql:/etc/{{.ProjectName}}/services/postfix/virtual-domains.cf
virtual_mailbox_maps = proxy:pgsql:/etc/{{.ProjectName}}/services/postfix/virtual-mailboxes.cf
virtual_alias_maps = proxy:pgsql:/etc/{{.ProjectName}}/services/postfix/virtual-aliases.cf
virtual_mailbox_base = /var/lib/{{.ProjectName}}/mail
virtual_uid_maps = static:1000
virtual_gid_maps = static:1000
virtual_minimum_uid = 1000

# Security and TLS
smtpd_tls_cert_file = /etc/{{.ProjectName}}/certs/{{.ServerDomain}}/fullchain.pem
smtpd_tls_key_file = /etc/{{.ProjectName}}/certs/{{.ServerDomain}}/privkey.pem
smtpd_use_tls = yes
smtpd_tls_session_cache_database = btree:${data_directory}/smtpd_scache
smtp_tls_session_cache_database = btree:${data_directory}/smtp_scache
smtpd_tls_security_level = may
smtp_tls_security_level = may
smtpd_tls_protocols = !SSLv2, !SSLv3, !TLSv1, !TLSv1.1
smtp_tls_protocols = !SSLv2, !SSLv3, !TLSv1, !TLSv1.1
smtpd_tls_mandatory_protocols = !SSLv2, !SSLv3, !TLSv1, !TLSv1.1

# SMTP restrictions
smtpd_helo_required = yes
smtpd_helo_restrictions = permit_mynetworks, reject_invalid_helo_hostname, reject_non_fqdn_helo_hostname
smtpd_sender_restrictions = permit_mynetworks, reject_non_fqdn_sender, reject_unknown_sender_domain
smtpd_recipient_restrictions = permit_mynetworks, permit_sasl_authenticated, reject_unauth_destination, reject_invalid_hostname, reject_non_fqdn_recipient

# SASL authentication
smtpd_sasl_auth_enable = yes
smtpd_sasl_type = dovecot
smtpd_sasl_path = private/auth
smtpd_sasl_security_options = noanonymous
smtpd_sasl_local_domain = $myhostname
broken_sasl_auth_clients = yes

# Message size limit (25MB)
message_size_limit = 25600000
mailbox_size_limit = 0

# Content filtering (will be configured for ClamAV and SpamAssassin)
# content_filter = scan:127.0.0.1:10025

# Logging
maillog_file = /var/log/{{.ProjectName}}/mail.log
`

	return s.GenerateConfig(template, "postfix", nil)
}

// GenerateBindConfig generates BIND named.conf configuration
func (s *Service) GenerateBindConfig() (string, error) {
	template := `// CASDC BIND configuration - Generated automatically
// Do not edit manually - changes will be overwritten

options {
    directory "{{.BindConfDir | default "/var/named"}}";
    dump-file "{{.BindConfDir}}/data/cache_dump.db";
    statistics-file "{{.BindConfDir}}/data/named_stats.txt";
    memstatistics-file "{{.BindConfDir}}/data/named_mem_stats.txt";

    // Listen on all interfaces
    listen-on port 53 { any; };
    listen-on-v6 port 53 { any; };

    // Allow queries from local network
    allow-query { any; };

    // Forwarders (can be configured in database)
    forwarders {
        8.8.8.8;
        8.8.4.4;
        1.1.1.1;
    };

    // DNSSEC
    dnssec-enable yes;
    dnssec-validation auto;

    // Recursion
    recursion yes;
    allow-recursion { any; };

    // Rate limiting
    rate-limit {
        responses-per-second 10;
    };
};

// Logging
logging {
    channel default_log {
        file "/var/log/{{.ProjectName}}/named.log" versions 3 size 5m;
        severity info;
        print-time yes;
        print-severity yes;
        print-category yes;
    };

    category default { default_log; };
    category queries { default_log; };
    category security { default_log; };
};

// Root hints
zone "." IN {
    type hint;
    file "{{.BindConfDir}}/named.ca";
};

// Localhost zones
zone "localhost" IN {
    type master;
    file "{{.BindConfDir}}/localhost.zone";
    allow-update { none; };
};

zone "0.0.127.in-addr.arpa" IN {
    type master;
    file "{{.BindConfDir}}/127.0.0.zone";
    allow-update { none; };
};

// Include CASDC managed zones
include "{{.BindConfDir}}/{{.ProjectName}}/zones.conf";
`

	return s.GenerateConfig(template, "bind", nil)
}

// GenerateDHCPConfig generates ISC DHCP server (dhcpd.conf) configuration
func (s *Service) GenerateDHCPConfig() (string, error) {
	template := `# CASDC DHCP Configuration - Generated automatically
# Do not edit manually - changes will be overwritten

# Global DHCP options
authoritative;
ddns-update-style interim;
ignore client-updates;

# Dynamic DNS updates to BIND
ddns-updates on;
ddns-domainname "{{.Domain}}";
ddns-rev-domainname "in-addr.arpa.";

# OMAPI configuration for lease management
omapi-port 7911;

# Logging
log-facility local7;

# Default lease times (can be overridden per scope)
default-lease-time 86400;      # 1 day
max-lease-time 604800;          # 7 days

# Include CASDC managed scopes
include "/etc/{{.ProjectName}}/services/dhcp/scopes.conf";
`

	return s.GenerateConfig(template, "dhcp", nil)
}

// GenerateDovecotConfig generates Dovecot IMAP/POP3 server configuration
func (s *Service) GenerateDovecotConfig() (string, error) {
	template := `# CASDC Dovecot Configuration - Generated automatically
# Do not edit manually - changes will be overwritten

# Protocols
protocols = imap pop3 lmtp

# Listen on all interfaces
listen = *, ::

# Mailbox location (Maildir format)
mail_location = maildir:/var/lib/{{.ProjectName}}/mail/%u

# Authentication
auth_mechanisms = plain login

# Password database (PostgreSQL/SQLite)
passdb {
  driver = sql
  args = /etc/{{.ProjectName}}/services/dovecot/dovecot-sql.conf.ext
}

# User database (PostgreSQL/SQLite)
userdb {
  driver = sql
  args = /etc/{{.ProjectName}}/services/dovecot/dovecot-sql.conf.ext
}

# SSL/TLS settings
ssl = required
ssl_cert = </etc/{{.ProjectName}}/certs/{{.ServerDomain}}/fullchain.pem
ssl_key = </etc/{{.ProjectName}}/certs/{{.ServerDomain}}/privkey.pem
ssl_min_protocol = TLSv1.2

# Logging
log_path = /var/log/{{.ProjectName}}/dovecot.log
info_log_path = /var/log/{{.ProjectName}}/dovecot-info.log
debug_log_path = /var/log/{{.ProjectName}}/dovecot-debug.log

# IMAP settings
protocol imap {
  mail_max_userip_connections = 50
  mail_plugins = $mail_plugins quota imap_quota
}

# POP3 settings
protocol pop3 {
  mail_max_userip_connections = 10
  mail_plugins = $mail_plugins quota
}

# LMTP settings for local delivery
protocol lmtp {
  mail_plugins = $mail_plugins quota sieve
}

# Quota configuration (5GB default)
plugin {
  quota = maildir:User quota
  quota_rule = *:storage=5GB
  quota_rule2 = Trash:storage=+1GB
  quota_warning = storage=95%% quota-warning 95 %u
  quota_warning2 = storage=80%% quota-warning 80 %u
}

# Sieve plugin for server-side filtering
plugin {
  sieve = ~/.dovecot.sieve
  sieve_dir = ~/sieve
}

# Service configuration
service imap-login {
  inet_listener imap {
    port = 143
  }
  inet_listener imaps {
    port = 993
    ssl = yes
  }
}

service pop3-login {
  inet_listener pop3 {
    port = 110
  }
  inet_listener pop3s {
    port = 995
    ssl = yes
  }
}

# SASL authentication for Postfix
service auth {
  unix_listener /var/spool/postfix/private/auth {
    mode = 0660
    user = postfix
    group = postfix
  }
}
`

	return s.GenerateConfig(template, "dovecot", nil)
}

// randomBytes generates random bytes for cryptographic operations
func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	if err != nil {
		return nil, err
	}
	return b, nil
}