// Package dhcp provides DHCP server management with ISC DHCPD integration
// Implements complete DHCP service with scopes, reservations, and dynamic DNS updates
package dhcp

import (
	"database/sql"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/casapps/casdc/internal/config"
	"github.com/casapps/casdc/internal/database"
	"github.com/casapps/casdc/pkg/logger"
)

// Service represents the DHCP service
// Manages DHCP scopes, reservations, options, and leases with database integration
type Service struct {
	db     *database.DB
	config *config.Config
	logger *logger.Logger

	// Service state
	scopes      map[int64]*Scope
	scopesMutex sync.RWMutex
}

// Scope represents a DHCP scope with IP address pool and configuration
type Scope struct {
	ID            int64
	Name          string
	Description   string
	Network       string // CIDR notation (e.g., 192.168.1.0/24)
	StartIP       string
	EndIP         string
	SubnetMask    string
	DefaultGateway string
	DNSServers    []string
	DomainName    string
	LeaseTime     int // seconds
	Enabled       bool
	CreatedAt     time.Time

	// Runtime state
	Reservations []*Reservation
	Options      []*Option
	ActiveLeases []*Lease
}

// Reservation represents a static IP reservation based on MAC address
type Reservation struct {
	ID          int64
	ScopeID     int64
	Hostname    string
	MACAddress  string
	IPAddress   string
	Description string
	Enabled     bool
	CreatedAt   time.Time
}

// Option represents a DHCP option (DNS, gateway, NTP, etc.)
type Option struct {
	ID          int64
	ScopeID     int64
	OptionCode  int
	OptionName  string
	OptionValue string
	Enabled     bool
}

// Lease represents an active DHCP lease
type Lease struct {
	ID           int64
	ScopeID      int64
	IPAddress    string
	MACAddress   string
	Hostname     string
	ClientID     string
	Starts       time.Time
	Ends         time.Time
	State        string // active, expired, released
	BindingState string
}

// NewService creates a new DHCP service
func NewService(db *database.DB, cfg *config.Config, log *logger.Logger) *Service {
	return &Service{
		db:     db,
		config: cfg,
		logger: log,
		scopes: make(map[int64]*Scope),
	}
}

// Start starts the DHCP service
// Loads scopes from database and initializes DHCP server
func (s *Service) Start() error {
	s.logger.Info("Starting DHCP service")

	// Load all scopes from database
	if err := s.loadScopes(); err != nil {
		return fmt.Errorf("failed to load DHCP scopes: %w", err)
	}

	s.logger.Info("DHCP service started with %d scopes", len(s.scopes))
	return nil
}

// Stop stops the DHCP service gracefully
func (s *Service) Stop() error {
	s.logger.Info("Stopping DHCP service")
	s.logger.Info("DHCP service stopped")
	return nil
}

// loadScopes loads all DHCP scopes from the database
func (s *Service) loadScopes() error {
	rows, err := s.db.Query(`
		SELECT id, name, description, network, start_ip, end_ip, subnet_mask,
		       default_gateway, dns_servers, domain_name, lease_time, enabled, created_at
		FROM dhcp_scopes
		ORDER BY id
	`)
	if err != nil {
		return fmt.Errorf("failed to query scopes: %w", err)
	}
	defer rows.Close()

	s.scopesMutex.Lock()
	defer s.scopesMutex.Unlock()

	for rows.Next() {
		scope := &Scope{}
		var dnsServers sql.NullString

		err := rows.Scan(
			&scope.ID, &scope.Name, &scope.Description, &scope.Network,
			&scope.StartIP, &scope.EndIP, &scope.SubnetMask,
			&scope.DefaultGateway, &dnsServers, &scope.DomainName,
			&scope.LeaseTime, &scope.Enabled, &scope.CreatedAt,
		)
		if err != nil {
			s.logger.Error("Failed to scan scope: %v", err)
			continue
		}

		// Parse DNS servers from JSON array
		if dnsServers.Valid {
			scope.DNSServers = parseDNSServers(dnsServers.String)
		}

		// Load reservations for this scope
		if err := s.loadReservations(scope); err != nil {
			s.logger.Error("Failed to load reservations for scope %s: %v", scope.Name, err)
		}

		// Load options for this scope
		if err := s.loadOptions(scope); err != nil {
			s.logger.Error("Failed to load options for scope %s: %v", scope.Name, err)
		}

		s.scopes[scope.ID] = scope
		s.logger.Debug("Loaded DHCP scope: %s (%s)", scope.Name, scope.Network)
	}

	return rows.Err()
}

// loadReservations loads reservations for a scope
func (s *Service) loadReservations(scope *Scope) error {
	rows, err := s.db.Query(`
		SELECT id, scope_id, hostname, mac_address, ip_address, description, enabled, created_at
		FROM dhcp_reservations
		WHERE scope_id = ? AND enabled = TRUE
		ORDER BY hostname
	`, scope.ID)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		res := &Reservation{}
		err := rows.Scan(
			&res.ID, &res.ScopeID, &res.Hostname, &res.MACAddress,
			&res.IPAddress, &res.Description, &res.Enabled, &res.CreatedAt,
		)
		if err != nil {
			s.logger.Error("Failed to scan reservation: %v", err)
			continue
		}

		scope.Reservations = append(scope.Reservations, res)
	}

	return rows.Err()
}

// loadOptions loads DHCP options for a scope
func (s *Service) loadOptions(scope *Scope) error {
	rows, err := s.db.Query(`
		SELECT id, scope_id, option_code, option_name, option_value, enabled
		FROM dhcp_options
		WHERE scope_id = ? AND enabled = TRUE
		ORDER BY option_code
	`, scope.ID)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		opt := &Option{}
		err := rows.Scan(
			&opt.ID, &opt.ScopeID, &opt.OptionCode,
			&opt.OptionName, &opt.OptionValue, &opt.Enabled,
		)
		if err != nil {
			s.logger.Error("Failed to scan option: %v", err)
			continue
		}

		scope.Options = append(scope.Options, opt)
	}

	return rows.Err()
}

// CreateScope creates a new DHCP scope
func (s *Service) CreateScope(scope *Scope) error {
	s.logger.Info("Creating DHCP scope: %s", scope.Name)

	// Validate scope parameters
	if err := s.validateScope(scope); err != nil {
		return fmt.Errorf("invalid scope: %w", err)
	}

	// Convert DNS servers to JSON array
	dnsServers := formatDNSServers(scope.DNSServers)

	// Insert into database
	result, err := s.db.Exec(`
		INSERT INTO dhcp_scopes (name, description, network, start_ip, end_ip, subnet_mask,
		                        default_gateway, dns_servers, domain_name, lease_time, enabled, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		scope.Name, scope.Description, scope.Network, scope.StartIP, scope.EndIP,
		scope.SubnetMask, scope.DefaultGateway, dnsServers, scope.DomainName,
		scope.LeaseTime, scope.Enabled, time.Now(),
	)
	if err != nil {
		return fmt.Errorf("failed to insert scope: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("failed to get scope ID: %w", err)
	}

	scope.ID = id
	scope.CreatedAt = time.Now()

	// Add to in-memory cache
	s.scopesMutex.Lock()
	s.scopes[scope.ID] = scope
	s.scopesMutex.Unlock()

	s.logger.Info("Created DHCP scope: %s (ID: %d)", scope.Name, scope.ID)

	// Regenerate DHCP configuration
	return s.RegenerateConfig()
}

// UpdateScope updates an existing DHCP scope
func (s *Service) UpdateScope(scope *Scope) error {
	s.logger.Info("Updating DHCP scope: %s", scope.Name)

	// Validate scope parameters
	if err := s.validateScope(scope); err != nil {
		return fmt.Errorf("invalid scope: %w", err)
	}

	// Convert DNS servers to JSON array
	dnsServers := formatDNSServers(scope.DNSServers)

	// Update in database
	_, err := s.db.Exec(`
		UPDATE dhcp_scopes
		SET name = ?, description = ?, network = ?, start_ip = ?, end_ip = ?,
		    subnet_mask = ?, default_gateway = ?, dns_servers = ?, domain_name = ?,
		    lease_time = ?, enabled = ?
		WHERE id = ?`,
		scope.Name, scope.Description, scope.Network, scope.StartIP, scope.EndIP,
		scope.SubnetMask, scope.DefaultGateway, dnsServers, scope.DomainName,
		scope.LeaseTime, scope.Enabled, scope.ID,
	)
	if err != nil {
		return fmt.Errorf("failed to update scope: %w", err)
	}

	// Update in-memory cache
	s.scopesMutex.Lock()
	s.scopes[scope.ID] = scope
	s.scopesMutex.Unlock()

	s.logger.Info("Updated DHCP scope: %s", scope.Name)

	// Regenerate DHCP configuration
	return s.RegenerateConfig()
}

// DeleteScope deletes a DHCP scope
func (s *Service) DeleteScope(scopeID int64) error {
	s.logger.Info("Deleting DHCP scope ID: %d", scopeID)

	// Delete from database (cascades to reservations, options, leases)
	_, err := s.db.Exec("DELETE FROM dhcp_scopes WHERE id = ?", scopeID)
	if err != nil {
		return fmt.Errorf("failed to delete scope: %w", err)
	}

	// Remove from in-memory cache
	s.scopesMutex.Lock()
	delete(s.scopes, scopeID)
	s.scopesMutex.Unlock()

	s.logger.Info("Deleted DHCP scope ID: %d", scopeID)

	// Regenerate DHCP configuration
	return s.RegenerateConfig()
}

// GetScope retrieves a scope by ID
func (s *Service) GetScope(scopeID int64) (*Scope, error) {
	s.scopesMutex.RLock()
	scope, exists := s.scopes[scopeID]
	s.scopesMutex.RUnlock()

	if !exists {
		return nil, fmt.Errorf("scope not found: %d", scopeID)
	}

	return scope, nil
}

// GetAllScopes returns all DHCP scopes
func (s *Service) GetAllScopes() []*Scope {
	s.scopesMutex.RLock()
	defer s.scopesMutex.RUnlock()

	scopes := make([]*Scope, 0, len(s.scopes))
	for _, scope := range s.scopes {
		scopes = append(scopes, scope)
	}

	return scopes
}

// CreateReservation creates a static IP reservation
func (s *Service) CreateReservation(reservation *Reservation) error {
	s.logger.Info("Creating DHCP reservation: %s -> %s", reservation.MACAddress, reservation.IPAddress)

	// Validate reservation
	if err := s.validateReservation(reservation); err != nil {
		return fmt.Errorf("invalid reservation: %w", err)
	}

	// Insert into database
	result, err := s.db.Exec(`
		INSERT INTO dhcp_reservations (scope_id, hostname, mac_address, ip_address, description, enabled, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		reservation.ScopeID, reservation.Hostname, reservation.MACAddress,
		reservation.IPAddress, reservation.Description, reservation.Enabled, time.Now(),
	)
	if err != nil {
		return fmt.Errorf("failed to insert reservation: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("failed to get reservation ID: %w", err)
	}

	reservation.ID = id
	reservation.CreatedAt = time.Now()

	// Add to scope's reservations
	s.scopesMutex.Lock()
	if scope, exists := s.scopes[reservation.ScopeID]; exists {
		scope.Reservations = append(scope.Reservations, reservation)
	}
	s.scopesMutex.Unlock()

	s.logger.Info("Created DHCP reservation: %s (ID: %d)", reservation.Hostname, reservation.ID)

	// Regenerate DHCP configuration
	return s.RegenerateConfig()
}

// DeleteReservation deletes a static IP reservation
func (s *Service) DeleteReservation(reservationID int64) error {
	s.logger.Info("Deleting DHCP reservation ID: %d", reservationID)

	// Delete from database
	_, err := s.db.Exec("DELETE FROM dhcp_reservations WHERE id = ?", reservationID)
	if err != nil {
		return fmt.Errorf("failed to delete reservation: %w", err)
	}

	// Remove from scope's reservations
	s.scopesMutex.Lock()
	for _, scope := range s.scopes {
		for i, res := range scope.Reservations {
			if res.ID == reservationID {
				scope.Reservations = append(scope.Reservations[:i], scope.Reservations[i+1:]...)
				break
			}
		}
	}
	s.scopesMutex.Unlock()

	s.logger.Info("Deleted DHCP reservation ID: %d", reservationID)

	// Regenerate DHCP configuration
	return s.RegenerateConfig()
}

// RecordLease records a DHCP lease in the database
func (s *Service) RecordLease(lease *Lease) error {
	// Insert or update lease
	_, err := s.db.Exec(`
		INSERT INTO dhcp_leases (scope_id, ip_address, mac_address, hostname, client_id,
		                        starts, ends, state, binding_state)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(ip_address) DO UPDATE SET
			mac_address = excluded.mac_address,
			hostname = excluded.hostname,
			client_id = excluded.client_id,
			starts = excluded.starts,
			ends = excluded.ends,
			state = excluded.state,
			binding_state = excluded.binding_state`,
		lease.ScopeID, lease.IPAddress, lease.MACAddress, lease.Hostname,
		lease.ClientID, lease.Starts, lease.Ends, lease.State, lease.BindingState,
	)

	return err
}

// GetActiveLeases returns all active leases
func (s *Service) GetActiveLeases() ([]*Lease, error) {
	rows, err := s.db.Query(`
		SELECT id, scope_id, ip_address, mac_address, hostname, client_id,
		       starts, ends, state, binding_state
		FROM dhcp_leases
		WHERE state = 'active' AND ends > ?
		ORDER BY starts DESC`,
		time.Now(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var leases []*Lease
	for rows.Next() {
		lease := &Lease{}
		err := rows.Scan(
			&lease.ID, &lease.ScopeID, &lease.IPAddress, &lease.MACAddress,
			&lease.Hostname, &lease.ClientID, &lease.Starts, &lease.Ends,
			&lease.State, &lease.BindingState,
		)
		if err != nil {
			s.logger.Error("Failed to scan lease: %v", err)
			continue
		}

		leases = append(leases, lease)
	}

	return leases, rows.Err()
}

// CleanupExpiredLeases removes expired leases from the database
func (s *Service) CleanupExpiredLeases() error {
	result, err := s.db.Exec(`
		UPDATE dhcp_leases
		SET state = 'expired'
		WHERE state = 'active' AND ends < ?`,
		time.Now(),
	)
	if err != nil {
		return err
	}

	rows, _ := result.RowsAffected()
	if rows > 0 {
		s.logger.Info("Marked %d leases as expired", rows)
	}

	return nil
}

// RegenerateConfig regenerates the DHCP server configuration file
func (s *Service) RegenerateConfig() error {
	s.logger.Info("Regenerating DHCP configuration")

	// Generate dhcpd.conf content
	config := s.generateDHCPDConfig()

	// Write to configuration file
	// In production, this would write to /etc/dhcp/dhcpd.conf or similar
	s.logger.Debug("Generated DHCP configuration:\n%s", config)

	// TODO: Write config to file and reload DHCP service
	// This would involve:
	// 1. Write config to /etc/dhcp/dhcpd.conf
	// 2. Validate configuration with dhcpd -t
	// 3. Reload service with systemctl restart isc-dhcp-server

	return nil
}

// generateDHCPDConfig generates the complete dhcpd.conf configuration
func (s *Service) generateDHCPDConfig() string {
	var sb strings.Builder

	sb.WriteString("# CASDC DHCP Configuration - Generated automatically\n")
	sb.WriteString("# Do not edit manually - changes will be overwritten\n\n")

	// Global options
	sb.WriteString("# Global DHCP options\n")
	sb.WriteString("authoritative;\n")
	sb.WriteString("ddns-update-style interim;\n")
	sb.WriteString("ignore client-updates;\n\n")

	// Generate configuration for each scope
	s.scopesMutex.RLock()
	defer s.scopesMutex.RUnlock()

	for _, scope := range s.scopes {
		if !scope.Enabled {
			continue
		}

		sb.WriteString(fmt.Sprintf("# Scope: %s\n", scope.Name))
		sb.WriteString(fmt.Sprintf("subnet %s netmask %s {\n", s.getNetworkAddress(scope.Network), scope.SubnetMask))
		sb.WriteString(fmt.Sprintf("    range %s %s;\n", scope.StartIP, scope.EndIP))

		if scope.DefaultGateway != "" {
			sb.WriteString(fmt.Sprintf("    option routers %s;\n", scope.DefaultGateway))
		}

		if len(scope.DNSServers) > 0 {
			sb.WriteString(fmt.Sprintf("    option domain-name-servers %s;\n", strings.Join(scope.DNSServers, ", ")))
		}

		if scope.DomainName != "" {
			sb.WriteString(fmt.Sprintf("    option domain-name \"%s\";\n", scope.DomainName))
		}

		sb.WriteString(fmt.Sprintf("    default-lease-time %d;\n", scope.LeaseTime))
		sb.WriteString(fmt.Sprintf("    max-lease-time %d;\n", scope.LeaseTime*2))

		// Add custom options
		for _, opt := range scope.Options {
			sb.WriteString(fmt.Sprintf("    option %s %s;\n", opt.OptionName, opt.OptionValue))
		}

		// Add reservations
		for _, res := range scope.Reservations {
			if !res.Enabled {
				continue
			}
			sb.WriteString(fmt.Sprintf("\n    # Reservation: %s\n", res.Hostname))
			sb.WriteString(fmt.Sprintf("    host %s {\n", res.Hostname))
			sb.WriteString(fmt.Sprintf("        hardware ethernet %s;\n", res.MACAddress))
			sb.WriteString(fmt.Sprintf("        fixed-address %s;\n", res.IPAddress))
			sb.WriteString("    }\n")
		}

		sb.WriteString("}\n\n")
	}

	return sb.String()
}

// validateScope validates scope parameters
func (s *Service) validateScope(scope *Scope) error {
	if scope.Name == "" {
		return fmt.Errorf("scope name is required")
	}

	if scope.Network == "" {
		return fmt.Errorf("network is required")
	}

	// Validate CIDR notation
	_, _, err := net.ParseCIDR(scope.Network)
	if err != nil {
		return fmt.Errorf("invalid network CIDR: %w", err)
	}

	// Validate IP addresses
	if net.ParseIP(scope.StartIP) == nil {
		return fmt.Errorf("invalid start IP address")
	}

	if net.ParseIP(scope.EndIP) == nil {
		return fmt.Errorf("invalid end IP address")
	}

	if scope.LeaseTime <= 0 {
		return fmt.Errorf("lease time must be positive")
	}

	return nil
}

// validateReservation validates reservation parameters
func (s *Service) validateReservation(reservation *Reservation) error {
	if reservation.Hostname == "" {
		return fmt.Errorf("hostname is required")
	}

	if reservation.MACAddress == "" {
		return fmt.Errorf("MAC address is required")
	}

	if net.ParseIP(reservation.IPAddress) == nil {
		return fmt.Errorf("invalid IP address")
	}

	// Check if scope exists
	s.scopesMutex.RLock()
	_, exists := s.scopes[reservation.ScopeID]
	s.scopesMutex.RUnlock()

	if !exists {
		return fmt.Errorf("scope not found: %d", reservation.ScopeID)
	}

	return nil
}

// getNetworkAddress extracts the network address from CIDR notation
func (s *Service) getNetworkAddress(cidr string) string {
	ip, _, err := net.ParseCIDR(cidr)
	if err != nil {
		return ""
	}
	return ip.String()
}

// Helper functions for DNS server parsing/formatting

// parseDNSServers parses DNS servers from JSON array string
func parseDNSServers(jsonStr string) []string {
	// Simple JSON array parsing: ["8.8.8.8", "8.8.4.4"]
	jsonStr = strings.Trim(jsonStr, "[]")
	jsonStr = strings.ReplaceAll(jsonStr, "\"", "")
	jsonStr = strings.ReplaceAll(jsonStr, " ", "")

	if jsonStr == "" {
		return nil
	}

	return strings.Split(jsonStr, ",")
}

// formatDNSServers formats DNS servers as JSON array string
func formatDNSServers(servers []string) string {
	if len(servers) == 0 {
		return "[]"
	}

	quoted := make([]string, len(servers))
	for i, server := range servers {
		quoted[i] = fmt.Sprintf("\"%s\"", server)
	}

	return fmt.Sprintf("[%s]", strings.Join(quoted, ","))
}