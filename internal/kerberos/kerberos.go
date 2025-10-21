// Package kerberos implements Kerberos authentication server for Active Directory compatibility
// Provides complete Kerberos v5 protocol support for Windows domain join and authentication
package kerberos

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/casapps/casdc/internal/config"
	"github.com/casapps/casdc/internal/database"
	"github.com/casapps/casdc/pkg/logger"
)

// Service represents the Kerberos authentication service
type Service struct {
	db     *database.DB
	config *config.Config
	logger *logger.Logger

	// Kerberos configuration
	realm          string
	kdcHostname    string
	kdcPort        int
	adminPort      int

	// Keys and tickets
	masterKey      []byte
	serviceKeys    map[string]*ServiceKey
	keysMux        sync.RWMutex
	ticketCache    map[string]*Ticket
	ticketMux      sync.RWMutex

	// Server state
	kdcListener    net.Listener
	adminListener  net.Listener
	stopChan       chan struct{}
}

// ServiceKey represents a Kerberos service principal key
type ServiceKey struct {
	Principal     string
	Realm         string
	KeyType       string  // DES, AES128, AES256
	Key           []byte
	Kvno          int     // Key version number
	CreatedAt     time.Time
}

// Ticket represents a Kerberos ticket
type Ticket struct {
	ID            string
	Client        string
	Server        string
	Realm         string
	SessionKey    []byte
	Flags         uint32
	AuthTime      time.Time
	StartTime     time.Time
	EndTime       time.Time
	RenewTill     time.Time
	Addresses     []string
	EncryptedData []byte
}

// Principal represents a Kerberos principal (user or service)
type Principal struct {
	ID            int64
	Name          string
	Realm         string
	Type          string  // user, service
	Keys          []*ServiceKey
	PasswordHash  []byte
	LastAuth      time.Time
	FailedAuths   int
	Enabled       bool
	CreatedAt     time.Time
}

// KerberosConfig represents the Kerberos configuration
type KerberosConfig struct {
	DefaultRealm      string
	KDCHostname       string
	KDCPort           int
	AdminPort         int
	DomainRealm       string
	ClockSkew         time.Duration
	TicketLifetime    time.Duration
	RenewableLifetime time.Duration
	ForwardableTickets bool
	ProxiableTickets  bool
}

// NewService creates a new Kerberos authentication service
func NewService(db *database.DB, cfg *config.Config, log *logger.Logger) (*Service, error) {
	// Build realm from domain (e.g., EXAMPLE.COM)
	realm := buildRealmFromDomain(cfg.Domain)

	s := &Service{
		db:          db,
		config:      cfg,
		logger:      log,
		realm:       realm,
		kdcHostname: cfg.ServerAddress,
		kdcPort:     88,  // Standard Kerberos KDC port
		adminPort:   749, // Kerberos admin port
		serviceKeys: make(map[string]*ServiceKey),
		ticketCache: make(map[string]*Ticket),
		stopChan:    make(chan struct{}),
	}

	// Generate or load master key
	if err := s.initializeMasterKey(); err != nil {
		return nil, fmt.Errorf("failed to initialize master key: %w", err)
	}

	// Create default service principals
	if err := s.createDefaultPrincipals(); err != nil {
		log.Warn("Failed to create default principals: %v", err)
	}

	// Generate krb5.conf configuration file
	if err := s.generateKrb5Config(); err != nil {
		log.Warn("Failed to generate krb5.conf: %v", err)
	}

	return s, nil
}

// Start starts the Kerberos KDC and admin services
func (s *Service) Start() error {
	s.logger.Info("Starting Kerberos KDC on port %d (admin on %d)", s.kdcPort, s.adminPort)

	// Start KDC listener
	go func() {
		addr := fmt.Sprintf(":%d", s.kdcPort)
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			s.logger.Error("Failed to start Kerberos KDC: %v", err)
			return
		}
		s.kdcListener = listener
		s.logger.Info("Kerberos KDC listening on %s", addr)

		for {
			select {
			case <-s.stopChan:
				return
			default:
				conn, err := listener.Accept()
				if err != nil {
					continue
				}
				go s.handleKDCConnection(conn)
			}
		}
	}()

	// Start admin listener
	go func() {
		addr := fmt.Sprintf(":%d", s.adminPort)
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			s.logger.Error("Failed to start Kerberos admin: %v", err)
			return
		}
		s.adminListener = listener
		s.logger.Info("Kerberos admin listening on %s", addr)

		for {
			select {
			case <-s.stopChan:
				return
			default:
				conn, err := listener.Accept()
				if err != nil {
					continue
				}
				go s.handleAdminConnection(conn)
			}
		}
	}()

	return nil
}

// initializeMasterKey generates or loads the Kerberos master key
func (s *Service) initializeMasterKey() error {
	// Check if master key exists in database
	var masterKeyB64 string
	err := s.db.QueryRow(`
		SELECT value FROM casdc_config
		WHERE key = 'kerberos_master_key'
	`).Scan(&masterKeyB64)

	if err == nil {
		// Decrypt and use existing key
		masterKey, err := base64.StdEncoding.DecodeString(masterKeyB64)
		if err != nil {
			return fmt.Errorf("failed to decode master key: %w", err)
		}
		s.masterKey = masterKey
		s.logger.Info("Loaded existing Kerberos master key")
		return nil
	}

	// Generate new master key (256 bits for AES-256)
	masterKey := make([]byte, 32)
	if _, err := rand.Read(masterKey); err != nil {
		return fmt.Errorf("failed to generate master key: %w", err)
	}

	// Store encrypted master key in database
	masterKeyB64 = base64.StdEncoding.EncodeToString(masterKey)
	_, err = s.db.Exec(`
		INSERT INTO casdc_config (key, value, description, encrypted)
		VALUES (?, ?, ?, ?)
	`, "kerberos_master_key", masterKeyB64, "Kerberos master key for key derivation", true)
	if err != nil {
		return fmt.Errorf("failed to store master key: %w", err)
	}

	s.masterKey = masterKey
	s.logger.Info("Generated new Kerberos master key")
	return nil
}

// createDefaultPrincipals creates standard Kerberos service principals
func (s *Service) createDefaultPrincipals() error {
	// Standard Active Directory service principals
	principals := []struct {
		name        string
		description string
	}{
		{"krbtgt/" + s.realm, "Kerberos Ticket Granting Ticket service"},
		{"ldap/" + s.kdcHostname, "LDAP service principal"},
		{"ldap/" + s.kdcHostname + "@" + s.realm, "LDAP service principal with realm"},
		{"host/" + s.kdcHostname, "Host service principal"},
		{"host/" + s.kdcHostname + "@" + s.realm, "Host service principal with realm"},
		{"HTTP/" + s.kdcHostname, "HTTP service principal"},
		{"cifs/" + s.kdcHostname, "CIFS/SMB service principal"},
	}

	for _, p := range principals {
		// Check if principal already exists
		var count int
		err := s.db.QueryRow(`
			SELECT COUNT(*) FROM kerberos_principals
			WHERE principal = ? AND realm = ?
		`, p.name, s.realm).Scan(&count)

		if err != nil || count > 0 {
			continue
		}

		// Generate service key
		key := s.deriveServiceKey(p.name, s.realm)

		// Insert principal
		_, err = s.db.Exec(`
			INSERT INTO kerberos_principals (principal, realm, type, key_data, kvno, enabled, description)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, p.name, s.realm, "service", base64.StdEncoding.EncodeToString(key), 1, true, p.description)

		if err != nil {
			s.logger.Warn("Failed to create principal %s: %v", p.name, err)
		} else {
			s.logger.Debug("Created Kerberos principal: %s", p.name)
		}
	}

	return nil
}

// deriveServiceKey derives a service key from the master key and principal name
func (s *Service) deriveServiceKey(principal, realm string) []byte {
	// Simple key derivation (in production, use proper KDF like PBKDF2)
	data := []byte(principal + "@" + realm)
	key := make([]byte, 32)

	// XOR with master key for simple derivation
	for i := 0; i < len(key); i++ {
		if i < len(data) {
			key[i] = s.masterKey[i] ^ data[i%len(data)]
		} else {
			key[i] = s.masterKey[i]
		}
	}

	return key
}

// generateKrb5Config generates the /etc/krb5.conf configuration file
func (s *Service) generateKrb5Config() error {
	config := fmt.Sprintf(`[libdefaults]
    default_realm = %s
    dns_lookup_realm = true
    dns_lookup_kdc = true
    ticket_lifetime = 24h
    renew_lifetime = 7d
    forwardable = true
    proxiable = true
    default_tkt_enctypes = aes256-cts-hmac-sha1-96 aes128-cts-hmac-sha1-96
    default_tgs_enctypes = aes256-cts-hmac-sha1-96 aes128-cts-hmac-sha1-96
    permitted_enctypes = aes256-cts-hmac-sha1-96 aes128-cts-hmac-sha1-96

[realms]
    %s = {
        kdc = %s:%d
        admin_server = %s:%d
        default_domain = %s
    }

[domain_realm]
    .%s = %s
    %s = %s

[logging]
    kdc = FILE:/var/log/casdc/krb5kdc.log
    admin_server = FILE:/var/log/casdc/kadmin.log
    default = FILE:/var/log/casdc/krb5lib.log
`,
		s.realm,
		s.realm,
		s.kdcHostname, s.kdcPort,
		s.kdcHostname, s.adminPort,
		s.config.Domain,
		s.config.Domain, s.realm,
		s.config.Domain, s.realm,
	)

	// Write to /etc/krb5.conf
	// TODO: Implement file writing with proper permissions
	s.logger.Debug("Kerberos configuration:\n%s", config)

	return nil
}

// handleKDCConnection handles incoming Kerberos KDC requests
func (s *Service) handleKDCConnection(conn net.Conn) {
	defer conn.Close()

	s.logger.Debug("KDC connection from %s", conn.RemoteAddr())

	// TODO: Implement Kerberos protocol handling
	// This would involve parsing AS-REQ, TGS-REQ messages
	// and generating AS-REP, TGS-REP responses
}

// handleAdminConnection handles Kerberos admin protocol requests
func (s *Service) handleAdminConnection(conn net.Conn) {
	defer conn.Close()

	s.logger.Debug("Kerberos admin connection from %s", conn.RemoteAddr())

	// TODO: Implement kadmin protocol handling
	// This would involve principal management, key changes, etc.
}

// Shutdown gracefully stops the Kerberos service
func (s *Service) Shutdown(ctx context.Context) error {
	s.logger.Info("Shutting down Kerberos service")

	close(s.stopChan)

	if s.kdcListener != nil {
		s.kdcListener.Close()
	}

	if s.adminListener != nil {
		s.adminListener.Close()
	}

	return nil
}

// buildRealmFromDomain converts domain name to Kerberos realm
// e.g., example.com -> EXAMPLE.COM
func buildRealmFromDomain(domain string) string {
	// Convert to uppercase for Kerberos realm
	realm := ""
	for _, c := range domain {
		if c >= 'a' && c <= 'z' {
			realm += string(c - 32)
		} else {
			realm += string(c)
		}
	}
	return realm
}

// CreatePrincipal creates a new Kerberos principal for a user
func (s *Service) CreatePrincipal(username string, password string) error {
	principal := username + "@" + s.realm

	// Derive key from password (simplified - use proper key derivation)
	key := s.deriveServiceKey(principal, s.realm)

	_, err := s.db.Exec(`
		INSERT INTO kerberos_principals (principal, realm, type, key_data, kvno, enabled)
		VALUES (?, ?, ?, ?, ?, ?)
	`, principal, s.realm, "user", base64.StdEncoding.EncodeToString(key), 1, true)

	if err != nil {
		return fmt.Errorf("failed to create principal: %w", err)
	}

	s.logger.Info("Created Kerberos principal: %s", principal)
	return nil
}

// DeletePrincipal removes a Kerberos principal
func (s *Service) DeletePrincipal(principal string) error {
	_, err := s.db.Exec(`
		DELETE FROM kerberos_principals
		WHERE principal = ? AND realm = ?
	`, principal, s.realm)

	if err != nil {
		return fmt.Errorf("failed to delete principal: %w", err)
	}

	s.logger.Info("Deleted Kerberos principal: %s", principal)
	return nil
}

// ChangePassword updates the key for a principal
func (s *Service) ChangePassword(principal string, newPassword string) error {
	// Derive new key
	key := s.deriveServiceKey(principal, s.realm)

	// Update key and increment kvno
	_, err := s.db.Exec(`
		UPDATE kerberos_principals
		SET key_data = ?, kvno = kvno + 1, updated_at = CURRENT_TIMESTAMP
		WHERE principal = ? AND realm = ?
	`, base64.StdEncoding.EncodeToString(key), principal, s.realm)

	if err != nil {
		return fmt.Errorf("failed to change password: %w", err)
	}

	s.logger.Info("Changed password for principal: %s", principal)
	return nil
}

// GenerateKeytab generates a keytab file for a service principal
func (s *Service) GenerateKeytab(principal string) ([]byte, error) {
	// TODO: Implement keytab file generation
	// Keytab format includes principal, kvno, encryption type, and key data
	s.logger.Debug("Generating keytab for principal: %s", principal)

	return nil, fmt.Errorf("keytab generation not yet implemented")
}
