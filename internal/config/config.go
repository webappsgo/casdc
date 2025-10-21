// Package config provides configuration management for CASDC
// Loading from environment variables, database, and providing defaults
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config represents the complete CASDC configuration
type Config struct {
	// Core Configuration
	Domain         string
	Organization   string
	AdminEmail     string
	Timezone       string
	ServerAddress  string
	ServerDomain   string

	// Directory Paths
	DataDir   string
	ConfigDir string
	LogDir    string
	WebDir    string
	BackupDir string

	// Database Configuration
	Database DatabaseConfig

	// Cache Configuration
	Cache CacheConfig

	// Certificate Management
	Certificate CertificateConfig

	// Security Configuration
	Security SecurityConfig

	// Network Configuration
	Network NetworkConfig

	// Mail Configuration
	Mail MailConfig

	// Backup Configuration
	Backup BackupConfig

	// Clustering Configuration
	ClusterMode    bool
	ClusterToken   string
	ClusterPrimary string
	NodeName       string
	NodeRole       string

	// Runtime Configuration
	Debug           bool
	LogLevel        string
	DevelopmentMode bool
	APIRateLimit    int
	Container       bool
}

// DatabaseConfig contains database connection settings
type DatabaseConfig struct {
	Type     string // sqlite, postgres, mariadb, mysql
	Host     string
	Port     int
	Name     string
	User     string
	Password string
	SSLMode  string
	MaxConns int
	MinConns int
}

// CacheConfig contains cache server settings
type CacheConfig struct {
	Type     string // none, valkey, redis
	Host     string
	Port     int
	Password string
	DB       int
	TTL      time.Duration
}

// CertificateConfig contains certificate management settings
type CertificateConfig struct {
	Provider    string // letsencrypt, internal, manual
	Email       string
	DNSProvider string
	APIToken    string
	CAKeyFile   string
	CACertFile  string
}

// SecurityConfig contains security-related settings
type SecurityConfig struct {
	Level              string // basic, standard, high, paranoid
	MFARequired        bool
	SessionTimeout     time.Duration
	PasswordComplexity string // low, medium, high, custom
	MinPasswordLength  int
	PasswordExpiry     int // days
	MaxLoginAttempts   int
	LockoutDuration    time.Duration
}

// NetworkConfig contains network-related settings
type NetworkConfig struct {
	ListenIP    string
	HTTPPort    int
	HTTPSPort   int
	ProxyMode   bool
	TrustedProxies []string
	IPv6Enabled bool
}

// MailConfig contains mail server settings
type MailConfig struct {
	Hostname   string
	Domain     string
	RelayHost  string
	RelayPort  int
	RelayUser  string
	RelayPass  string
	Quota      int64 // bytes
	MaxMsgSize int64 // bytes
}

// BackupConfig contains backup settings
type BackupConfig struct {
	Enabled     bool
	Schedule    string // cron format
	Retention   int    // days
	Compression bool
	Encryption  bool
	Destination string
}

// Load loads configuration from environment variables and provides defaults
func Load(configDir, dataDir string) (*Config, error) {
	cfg := &Config{
		// Set provided directories
		ConfigDir: configDir,
		DataDir:   dataDir,
		LogDir:    getEnv("CASDC_LOG_DIR", "/var/log/casdc"),
		WebDir:    getEnv("CASDC_WEB_DIR", "/var/www/default"),
		BackupDir: getEnv("CASDC_BACKUP_DIR", "/mnt/backups/casdc"),

		// Core Configuration with defaults
		Domain:       getEnv("CASDC_DOMAIN", "example.local"),
		Organization: getEnv("CASDC_ORGANIZATION", "My Organization"),
		AdminEmail:   getEnv("CASDC_ADMIN_EMAIL", "admin@example.local"),
		Timezone:     getEnv("CASDC_TIMEZONE", "UTC"),

		// Database defaults to SQLite
		Database: DatabaseConfig{
			Type:     getEnv("CASDC_DB_TYPE", "sqlite"),
			Host:     getEnv("CASDC_DB_HOST", "localhost"),
			Port:     getEnvInt("CASDC_DB_PORT", 5432),
			Name:     getEnv("CASDC_DB_NAME", "casdc"),
			User:     getEnv("CASDC_DB_USER", "casdc"),
			Password: getEnv("CASDC_DB_PASSWORD", ""),
			SSLMode:  getEnv("CASDC_DB_SSL_MODE", "disable"),
			MaxConns: getEnvInt("CASDC_DB_MAX_CONNS", 25),
			MinConns: getEnvInt("CASDC_DB_MIN_CONNS", 5),
		},

		// Cache defaults to none (optional enhancement)
		Cache: CacheConfig{
			Type:     getEnv("CASDC_CACHE_TYPE", "none"),
			Host:     getEnv("CASDC_CACHE_HOST", "localhost"),
			Port:     getEnvInt("CASDC_CACHE_PORT", 6379),
			Password: getEnv("CASDC_CACHE_PASSWORD", ""),
			DB:       getEnvInt("CASDC_CACHE_DB", 0),
			TTL:      time.Duration(getEnvInt("CASDC_CACHE_TTL", 3600)) * time.Second,
		},

		// Certificate defaults to internal CA
		Certificate: CertificateConfig{
			Provider:    getEnv("CASDC_CERT_PROVIDER", "internal"),
			Email:       getEnv("CASDC_CERT_EMAIL", ""),
			DNSProvider: getEnv("CASDC_DNS_PROVIDER", ""),
			APIToken:    getEnv("CASDC_DNS_API_TOKEN", ""),
		},

		// Security defaults to standard
		Security: SecurityConfig{
			Level:              getEnv("CASDC_SECURITY_LEVEL", "standard"),
			MFARequired:        getEnvBool("CASDC_MFA_REQUIRED", false),
			SessionTimeout:     time.Duration(getEnvInt("CASDC_SESSION_TIMEOUT", 28800)) * time.Second,
			PasswordComplexity: getEnv("CASDC_PASSWORD_COMPLEXITY", "high"),
			MinPasswordLength:  getEnvInt("CASDC_MIN_PASSWORD_LENGTH", 12),
			PasswordExpiry:     getEnvInt("CASDC_PASSWORD_EXPIRY", 90),
			MaxLoginAttempts:   getEnvInt("CASDC_MAX_LOGIN_ATTEMPTS", 5),
			LockoutDuration:    time.Duration(getEnvInt("CASDC_LOCKOUT_DURATION", 1800)) * time.Second,
		},

		// Network defaults
		Network: NetworkConfig{
			ListenIP:   getEnv("CASDC_LISTEN_IP", "0.0.0.0"),
			HTTPPort:   getEnvInt("CASDC_HTTP_PORT", 80),
			HTTPSPort:  getEnvInt("CASDC_HTTPS_PORT", 443),
			ProxyMode:  getEnvBool("CASDC_PROXY_MODE", false),
			TrustedProxies: strings.Split(getEnv("CASDC_TRUSTED_PROXIES",
				"127.0.0.0/8,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,::1/128,fc00::/7"), ","),
			IPv6Enabled: getEnvBool("CASDC_IPV6_ENABLED", detectIPv6()),
		},

		// Mail defaults
		Mail: MailConfig{
			Hostname:   getEnv("CASDC_MAIL_HOSTNAME", "mail."+getEnv("CASDC_DOMAIN", "example.local")),
			Domain:     getEnv("CASDC_MAIL_DOMAIN", getEnv("CASDC_DOMAIN", "example.local")),
			RelayHost:  getEnv("CASDC_SMTP_RELAY_HOST", ""),
			RelayPort:  getEnvInt("CASDC_SMTP_RELAY_PORT", 587),
			RelayUser:  getEnv("CASDC_SMTP_RELAY_USER", ""),
			RelayPass:  getEnv("CASDC_SMTP_RELAY_PASS", ""),
			Quota:      getEnvInt64("CASDC_MAIL_QUOTA", 5368709120),     // 5GB
			MaxMsgSize: getEnvInt64("CASDC_MAIL_MAX_MSG_SIZE", 25600000), // 25MB
		},

		// Backup defaults
		Backup: BackupConfig{
			Enabled:     getEnvBool("CASDC_BACKUP_ENABLED", true),
			Schedule:    getEnv("CASDC_BACKUP_SCHEDULE", "0 2 * * *"),
			Retention:   getEnvInt("CASDC_BACKUP_RETENTION", 30),
			Compression: getEnvBool("CASDC_BACKUP_COMPRESSION", true),
			Encryption:  getEnvBool("CASDC_BACKUP_ENCRYPTION", true),
			Destination: getEnv("CASDC_BACKUP_DESTINATION", "/mnt/backups/casdc"),
		},

		// Clustering
		ClusterMode:    getEnvBool("CASDC_CLUSTER_MODE", false),
		ClusterToken:   getEnv("CASDC_CLUSTER_TOKEN", ""),
		ClusterPrimary: getEnv("CASDC_CLUSTER_PRIMARY", ""),
		NodeName:       getEnv("CASDC_NODE_NAME", getHostname()),
		NodeRole:       getEnv("CASDC_NODE_ROLE", "primary"),

		// Runtime
		Debug:           getEnvBool("CASDC_DEBUG", false),
		LogLevel:        getEnv("CASDC_LOG_LEVEL", "warn"),
		DevelopmentMode: getEnvBool("CASDC_DEVELOPMENT_MODE", false),
		APIRateLimit:    getEnvInt("CASDC_API_RATE_LIMIT", 60),
		Container:       getEnvBool("CASDC_CONTAINER", isContainer()),
	}

	// Auto-detect server address if not set
	if cfg.ServerAddress == "" {
		cfg.ServerAddress = detectServerAddress()
	}

	// Set server domain
	cfg.ServerDomain = fmt.Sprintf("casdc.%s", cfg.Domain)

	// Adjust SQLite path if using SQLite
	if cfg.Database.Type == "sqlite" && cfg.Database.Name == "casdc" {
		cfg.Database.Name = filepath.Join(dataDir, "casdc.db")
	}

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("configuration validation failed: %w", err)
	}

	return cfg, nil
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	// Check required fields
	if c.Domain == "" {
		return fmt.Errorf("domain is required")
	}

	if c.AdminEmail == "" {
		return fmt.Errorf("admin email is required")
	}

	// Validate email format
	if !strings.Contains(c.AdminEmail, "@") {
		return fmt.Errorf("invalid admin email format")
	}

	// Validate database configuration
	switch c.Database.Type {
	case "sqlite":
		// SQLite requires no additional validation
	case "postgres", "mariadb", "mysql":
		if c.Database.Host == "" {
			return fmt.Errorf("database host is required for %s", c.Database.Type)
		}
		if c.Database.User == "" {
			return fmt.Errorf("database user is required for %s", c.Database.Type)
		}
	default:
		return fmt.Errorf("unsupported database type: %s", c.Database.Type)
	}

	// Validate cache configuration
	switch c.Cache.Type {
	case "none", "valkey", "redis":
		// Valid cache types
	default:
		return fmt.Errorf("unsupported cache type: %s", c.Cache.Type)
	}

	// Validate certificate provider
	switch c.Certificate.Provider {
	case "letsencrypt":
		if c.Certificate.Email == "" {
			c.Certificate.Email = c.AdminEmail
		}
		if c.Certificate.DNSProvider != "" && c.Certificate.APIToken == "" {
			return fmt.Errorf("DNS API token required for DNS provider %s", c.Certificate.DNSProvider)
		}
	case "internal", "manual":
		// Valid providers
	default:
		return fmt.Errorf("unsupported certificate provider: %s", c.Certificate.Provider)
	}

	// Validate security level
	switch c.Security.Level {
	case "basic", "standard", "high", "paranoid":
		// Valid security levels
	default:
		return fmt.Errorf("unsupported security level: %s", c.Security.Level)
	}

	// Validate clustering configuration
	if c.ClusterMode {
		if c.NodeRole == "secondary" && c.ClusterPrimary == "" {
			return fmt.Errorf("cluster primary address required for secondary node")
		}
		if c.NodeRole == "secondary" && c.ClusterToken == "" {
			return fmt.Errorf("cluster token required for secondary node")
		}
	}

	// Validate port availability
	if c.Network.HTTPPort < 0 || c.Network.HTTPPort > 65535 {
		return fmt.Errorf("invalid HTTP port: %d", c.Network.HTTPPort)
	}
	if c.Network.HTTPSPort < 0 || c.Network.HTTPSPort > 65535 {
		return fmt.Errorf("invalid HTTPS port: %d", c.Network.HTTPSPort)
	}

	return nil
}

// GetDSN returns the database connection string
func (c *DatabaseConfig) GetDSN() string {
	switch c.Type {
	case "sqlite":
		return c.Name
	case "postgres":
		return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
			c.Host, c.Port, c.User, c.Password, c.Name, c.SSLMode)
	case "mysql", "mariadb":
		return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true",
			c.User, c.Password, c.Host, c.Port, c.Name)
	default:
		return ""
	}
}

// Helper functions for environment variable parsing

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intVal, err := strconv.Atoi(value); err == nil {
			return intVal
		}
	}
	return defaultValue
}

func getEnvInt64(key string, defaultValue int64) int64 {
	if value := os.Getenv(key); value != "" {
		if intVal, err := strconv.ParseInt(value, 10, 64); err == nil {
			return intVal
		}
	}
	return defaultValue
}

func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if boolVal, err := strconv.ParseBool(value); err == nil {
			return boolVal
		}
	}
	return defaultValue
}

// detectServerAddress attempts to detect the server's IP address
func detectServerAddress() string {
	// Try to get IP from default route
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err == nil {
		defer conn.Close()
		localAddr := conn.LocalAddr().(*net.UDPAddr)
		return localAddr.IP.String()
	}

	// Fallback to hostname
	hostname, _ := os.Hostname()
	return hostname
}

// detectIPv6 checks if the system has a valid public IPv6 address
func detectIPv6() bool {
	interfaces, err := net.Interfaces()
	if err != nil {
		return false
	}

	for _, iface := range interfaces {
		// Skip loopback and down interfaces
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok {
				if ipNet.IP.To4() == nil && ipNet.IP.IsGlobalUnicast() {
					return true
				}
			}
		}
	}

	return false
}

// isContainer attempts to detect if running in a container
func isContainer() bool {
	// Check for Docker
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}

	// Check for Kubernetes
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return true
	}

	// Check for containerd
	if _, err := os.Stat("/run/containerd"); err == nil {
		return true
	}

	return false
}

// getHostname returns the system hostname or a default value
func getHostname() string {
	hostname, err := os.Hostname()
	if err != nil {
		return "casdc-node"
	}
	return hostname
}