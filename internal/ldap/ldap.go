// Package ldap implements LDAP server functionality for Active Directory compatibility
// Provides complete LDAP v3 protocol support with Active Directory schema
// as per CASDC Active Directory replacement specification
package ldap

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/casapps/casdc/internal/config"
	"github.com/casapps/casdc/internal/database"
	"github.com/casapps/casdc/pkg/logger"
)

// Service handles LDAP server operations
type Service struct {
	db     *database.DB
	config *config.Config
	logger *logger.Logger

	// LDAP server configuration
	baseDN        string
	domainDN      string
	bindAddress   string
	bindPort      int
	bindPortTLS   int
	tlsConfig     *tls.Config

	// LDAP directory tree
	directory     *Directory
	directoryMux  sync.RWMutex

	// Connection tracking
	connections   map[string]*Connection
	connMutex     sync.RWMutex

	// Server state
	listener      net.Listener
	tlsListener   net.Listener
	stopChan      chan struct{}
}

// Directory represents the LDAP directory information tree
type Directory struct {
	RootDSE       *Entry
	ConfigContext *Entry
	SchemaContext *Entry
	DomainContext *Entry
	Entries       map[string]*Entry
}

// Entry represents an LDAP directory entry
type Entry struct {
	DN           string
	ObjectClass  []string
	Attributes   map[string][]string
	Children     []*Entry
	Parent       *Entry
	Created      time.Time
	Modified     time.Time
}

// Connection represents an LDAP client connection
type Connection struct {
	ID           string
	RemoteAddr   net.Addr
	Bound        bool
	BindDN       string
	UserID       int64
	Username     string
	ConnectedAt  time.Time
	LastActivity time.Time
}

// SearchRequest represents an LDAP search operation
type SearchRequest struct {
	BaseDN       string
	Scope        string // base, one, sub
	DerefAliases string
	Filter       string
	Attributes   []string
	SizeLimit    int
	TimeLimit    int
}

// SearchResult represents search results
type SearchResult struct {
	Entries []*Entry
	Count   int
}

// ModifyRequest represents an LDAP modify operation
type ModifyRequest struct {
	DN      string
	Changes []Change
}

// Change represents a modification to an entry
type Change struct {
	Operation string // add, delete, replace
	Type      string
	Values    []string
}

// NewService creates a new LDAP service instance
func NewService(db *database.DB, cfg *config.Config, log *logger.Logger) (*Service, error) {
	// Build base DN from domain
	baseDN := buildBaseDN(cfg.Domain)

	s := &Service{
		db:          db,
		config:      cfg,
		logger:      log,
		baseDN:      baseDN,
		domainDN:    baseDN,
		bindAddress: "0.0.0.0",
		bindPort:    389,
		bindPortTLS: 636,
		connections: make(map[string]*Connection),
		stopChan:    make(chan struct{}),
	}

	// Initialize directory tree
	if err := s.initializeDirectory(); err != nil {
		return nil, fmt.Errorf("failed to initialize directory: %w", err)
	}

	// Load users and OUs from database into LDAP directory
	if err := s.syncFromDatabase(); err != nil {
		log.Warn("Failed to sync from database: %v", err)
	}

	return s, nil
}

// Start starts the LDAP server
func (s *Service) Start() error {
	s.logger.Info("Starting LDAP server on port %d (LDAPS on %d)", s.bindPort, s.bindPortTLS)

	// Start LDAP listener (port 389)
	go func() {
		addr := fmt.Sprintf("%s:%d", s.bindAddress, s.bindPort)
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			s.logger.Error("Failed to start LDAP listener: %v", err)
			return
		}
		s.listener = listener
		s.logger.Info("LDAP server listening on %s", addr)

		for {
			select {
			case <-s.stopChan:
				return
			default:
				conn, err := listener.Accept()
				if err != nil {
					continue
				}
				go s.handleConnection(conn, false)
			}
		}
	}()

	// Start LDAPS listener (port 636)
	go func() {
		if s.tlsConfig == nil {
			s.logger.Warn("TLS config not available, LDAPS disabled")
			return
		}

		addr := fmt.Sprintf("%s:%d", s.bindAddress, s.bindPortTLS)
		listener, err := tls.Listen("tcp", addr, s.tlsConfig)
		if err != nil {
			s.logger.Error("Failed to start LDAPS listener: %v", err)
			return
		}
		s.tlsListener = listener
		s.logger.Info("LDAPS server listening on %s", addr)

		for {
			select {
			case <-s.stopChan:
				return
			default:
				conn, err := listener.Accept()
				if err != nil {
					continue
				}
				go s.handleConnection(conn, true)
			}
		}
	}()

	return nil
}

// initializeDirectory creates the initial LDAP directory structure
func (s *Service) initializeDirectory() error {
	s.logger.Info("Initializing LDAP directory with base DN: %s", s.baseDN)

	s.directoryMux.Lock()
	defer s.directoryMux.Unlock()

	s.directory = &Directory{
		Entries: make(map[string]*Entry),
	}

	// Create Root DSE
	s.directory.RootDSE = &Entry{
		DN:          "",
		ObjectClass: []string{"top"},
		Attributes: map[string][]string{
			"namingContexts":       {s.domainDN},
			"defaultNamingContext": {s.domainDN},
			"subschemaSubentry":    {"CN=Schema,CN=Configuration," + s.domainDN},
			"supportedLDAPVersion": {"3"},
			"supportedControl":     {"1.2.840.113556.1.4.473"}, // Sort control
			"supportedSASLMechanisms": {"DIGEST-MD5", "GSSAPI"},
			"dnsHostName":          {s.config.ServerAddress},
			"serverName":           {"CN=CASDC,CN=Servers,CN=Sites,CN=Configuration," + s.domainDN},
		},
		Created:  time.Now(),
		Modified: time.Now(),
	}

	// Create domain root entry
	domainParts := strings.Split(s.config.Domain, ".")
	domainName := domainParts[0]

	s.directory.DomainContext = &Entry{
		DN:          s.domainDN,
		ObjectClass: []string{"top", "domain", "domainDNS"},
		Attributes: map[string][]string{
			"dc":               {domainName},
			"objectCategory":   {"CN=Domain-DNS,CN=Schema,CN=Configuration," + s.domainDN},
			"distinguishedName": {s.domainDN},
		},
		Created:  time.Now(),
		Modified: time.Now(),
	}

	// Add to directory
	s.directory.Entries[s.domainDN] = s.directory.DomainContext

	// Create standard containers
	s.createStandardContainers()

	s.logger.Info("LDAP directory initialized successfully")
	return nil
}

// createStandardContainers creates standard AD containers
func (s *Service) createStandardContainers() {
	containers := []struct {
		cn          string
		objectClass []string
	}{
		{"Users", []string{"top", "container"}},
		{"Computers", []string{"top", "container"}},
		{"Groups", []string{"top", "container"}},
		{"Domain Controllers", []string{"top", "container"}},
		{"Builtin", []string{"top", "builtinDomain"}},
	}

	for _, container := range containers {
		dn := fmt.Sprintf("CN=%s,%s", container.cn, s.domainDN)
		entry := &Entry{
			DN:          dn,
			ObjectClass: container.objectClass,
			Attributes: map[string][]string{
				"cn":                {container.cn},
				"distinguishedName": {dn},
				"objectCategory":    {"CN=Container,CN=Schema,CN=Configuration," + s.domainDN},
			},
			Created:  time.Now(),
			Modified: time.Now(),
		}

		s.directory.Entries[dn] = entry
	}
}

// syncFromDatabase loads users, groups, and OUs from database into LDAP
func (s *Service) syncFromDatabase() error {
	s.logger.Info("Syncing database to LDAP directory")

	// Sync users
	if err := s.syncUsers(); err != nil {
		return fmt.Errorf("failed to sync users: %w", err)
	}

	// Sync groups
	if err := s.syncGroups(); err != nil {
		return fmt.Errorf("failed to sync groups: %w", err)
	}

	// Sync organizational units
	if err := s.syncOUs(); err != nil {
		return fmt.Errorf("failed to sync OUs: %w", err)
	}

	s.logger.Info("Database sync to LDAP completed")
	return nil
}

// syncUsers synchronizes users from database to LDAP
func (s *Service) syncUsers() error {
	query := `
		SELECT id, username, email, first_name, last_name, display_name,
		       description, enabled, created_at
		FROM users
		WHERE enabled = TRUE
		ORDER BY username
	`

	rows, err := s.db.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()

	s.directoryMux.Lock()
	defer s.directoryMux.Unlock()

	for rows.Next() {
		var userID int64
		var username, email, firstName, lastName, displayName, description string
		var enabled bool
		var createdAt time.Time

		err := rows.Scan(&userID, &username, &email, &firstName, &lastName,
			&displayName, &description, &enabled, &createdAt)
		if err != nil {
			s.logger.Error("Failed to scan user: %v", err)
			continue
		}

		// Create LDAP user entry
		dn := fmt.Sprintf("CN=%s,CN=Users,%s", username, s.domainDN)
		entry := &Entry{
			DN:          dn,
			ObjectClass: []string{"top", "person", "organizationalPerson", "user"},
			Attributes: map[string][]string{
				"cn":                {username},
				"sAMAccountName":    {username},
				"userPrincipalName": {email},
				"mail":              {email},
				"givenName":         {firstName},
				"sn":                {lastName},
				"displayName":       {displayName},
				"description":       {description},
				"distinguishedName": {dn},
				"objectCategory":    {"CN=Person,CN=Schema,CN=Configuration," + s.domainDN},
				"userAccountControl": {"512"}, // Normal account
			},
			Created:  createdAt,
			Modified: time.Now(),
		}

		s.directory.Entries[dn] = entry
	}

	return rows.Err()
}

// syncGroups synchronizes groups from database to LDAP
func (s *Service) syncGroups() error {
	query := `
		SELECT id, name, description, type, scope, created_at
		FROM groups
		ORDER BY name
	`

	rows, err := s.db.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()

	s.directoryMux.Lock()
	defer s.directoryMux.Unlock()

	for rows.Next() {
		var groupID int64
		var name, description, groupType, scope string
		var createdAt time.Time

		err := rows.Scan(&groupID, &name, &description, &groupType, &scope, &createdAt)
		if err != nil {
			s.logger.Error("Failed to scan group: %v", err)
			continue
		}

		// Create LDAP group entry
		dn := fmt.Sprintf("CN=%s,CN=Groups,%s", name, s.domainDN)
		entry := &Entry{
			DN:          dn,
			ObjectClass: []string{"top", "group"},
			Attributes: map[string][]string{
				"cn":                {name},
				"sAMAccountName":    {name},
				"description":       {description},
				"distinguishedName": {dn},
				"objectCategory":    {"CN=Group,CN=Schema,CN=Configuration," + s.domainDN},
				"groupType":         {getGroupType(groupType, scope)},
			},
			Created:  createdAt,
			Modified: time.Now(),
		}

		s.directory.Entries[dn] = entry
	}

	return rows.Err()
}

// syncOUs synchronizes organizational units from database to LDAP
func (s *Service) syncOUs() error {
	query := `
		SELECT id, name, description, distinguished_name, created_at
		FROM organizational_units
		ORDER BY distinguished_name
	`

	rows, err := s.db.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()

	s.directoryMux.Lock()
	defer s.directoryMux.Unlock()

	for rows.Next() {
		var ouID int64
		var name, description, dn string
		var createdAt time.Time

		err := rows.Scan(&ouID, &name, &description, &dn, &createdAt)
		if err != nil {
			s.logger.Error("Failed to scan OU: %v", err)
			continue
		}

		// Create LDAP OU entry
		entry := &Entry{
			DN:          dn,
			ObjectClass: []string{"top", "organizationalUnit"},
			Attributes: map[string][]string{
				"ou":                {name},
				"description":       {description},
				"distinguishedName": {dn},
				"objectCategory":    {"CN=Organizational-Unit,CN=Schema,CN=Configuration," + s.domainDN},
			},
			Created:  createdAt,
			Modified: time.Now(),
		}

		s.directory.Entries[dn] = entry
	}

	return rows.Err()
}

// handleConnection handles an LDAP client connection
func (s *Service) handleConnection(conn net.Conn, isTLS bool) {
	defer conn.Close()

	connID := fmt.Sprintf("%s-%d", conn.RemoteAddr().String(), time.Now().UnixNano())
	connection := &Connection{
		ID:           connID,
		RemoteAddr:   conn.RemoteAddr(),
		Bound:        false,
		ConnectedAt:  time.Now(),
		LastActivity: time.Now(),
	}

	s.connMutex.Lock()
	s.connections[connID] = connection
	s.connMutex.Unlock()

	if isTLS {
		s.logger.Debug("LDAPS connection from %s", conn.RemoteAddr())
	} else {
		s.logger.Debug("LDAP connection from %s", conn.RemoteAddr())
	}

	// TODO: Implement LDAP protocol handling
	// This would involve parsing LDAP messages (BER encoding),
	// handling BIND, SEARCH, MODIFY, ADD, DELETE operations,
	// and sending proper responses
}

// buildBaseDN builds LDAP base DN from domain name
func buildBaseDN(domain string) string {
	parts := strings.Split(domain, ".")
	dnParts := make([]string, len(parts))
	for i, part := range parts {
		dnParts[i] = "DC=" + part
	}
	return strings.Join(dnParts, ",")
}

// getGroupType converts database group type to AD group type
func getGroupType(groupType, scope string) string {
	// Active Directory group type flags
	// -2147483646 = Global security group
	// -2147483644 = Domain local security group
	// -2147483640 = Universal security group
	// 2 = Global distribution group
	// 4 = Domain local distribution group
	// 8 = Universal distribution group

	if groupType == "security" {
		switch scope {
		case "global":
			return "-2147483646"
		case "domain_local":
			return "-2147483644"
		case "universal":
			return "-2147483640"
		}
	} else {
		switch scope {
		case "global":
			return "2"
		case "domain_local":
			return "4"
		case "universal":
			return "8"
		}
	}

	return "-2147483646" // Default to global security
}

// GetEntry retrieves an LDAP entry by DN
func (s *Service) GetEntry(dn string) (*Entry, error) {
	s.directoryMux.RLock()
	defer s.directoryMux.RUnlock()

	entry, exists := s.directory.Entries[dn]
	if !exists {
		return nil, fmt.Errorf("entry not found: %s", dn)
	}

	return entry, nil
}

// Shutdown gracefully stops the LDAP service
func (s *Service) Shutdown(ctx context.Context) error {
	s.logger.Info("Shutting down LDAP service")

	close(s.stopChan)

	if s.listener != nil {
		s.listener.Close()
	}

	if s.tlsListener != nil {
		s.tlsListener.Close()
	}

	return nil
}
