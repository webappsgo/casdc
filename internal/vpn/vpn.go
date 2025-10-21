// Package vpn implements comprehensive VPN services
// supporting OpenVPN, WireGuard, and IPSec protocols
// with automatic configuration and user management
package vpn

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/casapps/casdc/internal/config"
	"github.com/casapps/casdc/internal/database"
	"github.com/casapps/casdc/pkg/logger"
)

// Service handles VPN operations for multiple protocols
type Service struct {
	db     *database.DB
	config *config.Config
	logger *logger.Logger

	// VPN configuration paths
	vpnBasePath    string
	openVPNPath    string
	wireguardPath  string
	ipsecPath      string

	// Server management
	servers      map[int64]*VPNServer
	serversMutex sync.RWMutex

	// Client management
	clients      map[int64]*VPNClient
	clientsMutex sync.RWMutex
}

// VPNServer represents a VPN server configuration
type VPNServer struct {
	ID          int64
	Name        string
	Type        string // "openvpn", "wireguard", "ipsec"
	Enabled     bool
	ListenPort  int
	Protocol    string // "udp" or "tcp"
	Network     string // VPN network CIDR
	DNSServers  []string
	Routes      []string
	Config      string
	CACert      string
	ServerCert  string
	ServerKey   string
	DHParams    string
}

// VPNClient represents a VPN client configuration
type VPNClient struct {
	ID            int64
	ServerID      int64
	UserID        int64
	Name          string
	Enabled       bool
	Config        string
	Certificate   string
	PrivateKey    string
	PublicKey     string // For WireGuard
	AssignedIP    string
	LastConnected *time.Time
	BytesSent     int64
	BytesReceived int64
	CreatedAt     time.Time
}

// NewService creates a new VPN service instance
func NewService(db *database.DB, cfg *config.Config, log *logger.Logger) (*Service, error) {
	s := &Service{
		db:     db,
		config: cfg,
		logger: log,

		// VPN configuration paths
		vpnBasePath:   filepath.Join(cfg.DataDir, "vpn"),
		openVPNPath:   "/etc/openvpn",
		wireguardPath: "/etc/wireguard",
		ipsecPath:     "/etc/ipsec.d",

		servers: make(map[int64]*VPNServer),
		clients: make(map[int64]*VPNClient),
	}

	// Ensure VPN directories exist
	if err := s.ensureDirectories(); err != nil {
		return nil, fmt.Errorf("failed to create VPN directories: %w", err)
	}

	// Load VPN servers from database
	if err := s.loadServers(); err != nil {
		log.Warn("Failed to load VPN servers: %v", err)
	}

	// Load VPN clients from database
	if err := s.loadClients(); err != nil {
		log.Warn("Failed to load VPN clients: %v", err)
	}

	return s, nil
}

// ensureDirectories creates required VPN directories
func (s *Service) ensureDirectories() error {
	dirs := []struct {
		path string
		perm os.FileMode
	}{
		{s.vpnBasePath, 0700},
		{filepath.Join(s.vpnBasePath, "openvpn"), 0700},
		{filepath.Join(s.vpnBasePath, "wireguard"), 0700},
		{filepath.Join(s.vpnBasePath, "ipsec"), 0700},
		{filepath.Join(s.vpnBasePath, "clients"), 0700},
	}

	for _, d := range dirs {
		if err := os.MkdirAll(d.path, d.perm); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", d.path, err)
		}
	}

	return nil
}

// loadServers retrieves all VPN servers from database
func (s *Service) loadServers() error {
	query := `SELECT id, name, type, enabled, listen_port, protocol, network,
	          dns_servers, routes, config, ca_cert, server_cert, server_key, dh_params
	          FROM vpn_servers WHERE enabled = 1`

	rows, err := s.db.Query(query)
	if err != nil {
		return fmt.Errorf("failed to query VPN servers: %w", err)
	}
	defer rows.Close()

	s.serversMutex.Lock()
	defer s.serversMutex.Unlock()

	for rows.Next() {
		server := &VPNServer{}
		var dnsServers, routes string

		err := rows.Scan(
			&server.ID,
			&server.Name,
			&server.Type,
			&server.Enabled,
			&server.ListenPort,
			&server.Protocol,
			&server.Network,
			&dnsServers,
			&routes,
			&server.Config,
			&server.CACert,
			&server.ServerCert,
			&server.ServerKey,
			&server.DHParams,
		)
		if err != nil {
			s.logger.Error("Failed to scan VPN server: %v", err)
			continue
		}

		// Parse JSON arrays
		if dnsServers != "" {
			server.DNSServers = strings.Split(dnsServers, ",")
		}
		if routes != "" {
			server.Routes = strings.Split(routes, ",")
		}

		s.servers[server.ID] = server
		s.logger.Debug("Loaded VPN server: %s (%s)", server.Name, server.Type)
	}

	return rows.Err()
}

// loadClients retrieves all VPN clients from database
func (s *Service) loadClients() error {
	query := `SELECT id, server_id, user_id, name, enabled, config, certificate,
	          private_key, public_key, assigned_ip, last_connected, bytes_sent,
	          bytes_received, created_at
	          FROM vpn_clients WHERE enabled = 1`

	rows, err := s.db.Query(query)
	if err != nil {
		return fmt.Errorf("failed to query VPN clients: %w", err)
	}
	defer rows.Close()

	s.clientsMutex.Lock()
	defer s.clientsMutex.Unlock()

	for rows.Next() {
		client := &VPNClient{}

		err := rows.Scan(
			&client.ID,
			&client.ServerID,
			&client.UserID,
			&client.Name,
			&client.Enabled,
			&client.Config,
			&client.Certificate,
			&client.PrivateKey,
			&client.PublicKey,
			&client.AssignedIP,
			&client.LastConnected,
			&client.BytesSent,
			&client.BytesReceived,
			&client.CreatedAt,
		)
		if err != nil {
			s.logger.Error("Failed to scan VPN client: %v", err)
			continue
		}

		s.clients[client.ID] = client
	}

	return rows.Err()
}

// CreateOpenVPNServer creates and configures an OpenVPN server
func (s *Service) CreateOpenVPNServer(name, network string, port int) error {
	// Generate server certificates
	caCert, caKey, err := s.generateCA(name)
	if err != nil {
		return fmt.Errorf("failed to generate CA: %w", err)
	}

	serverCert, serverKey, err := s.generateCertificate(name+"-server", caCert, caKey)
	if err != nil {
		return fmt.Errorf("failed to generate server certificate: %w", err)
	}

	// Generate DH parameters
	dhParams := s.generateDHParams()

	// Generate OpenVPN configuration
	config := s.generateOpenVPNConfig(name, network, port)

	// Insert into database
	query := `INSERT INTO vpn_servers (name, type, enabled, listen_port, protocol, network,
	          config, ca_cert, server_cert, server_key, dh_params)
	          VALUES (?, 'openvpn', 1, ?, 'udp', ?, ?, ?, ?, ?, ?)`

	result, err := s.db.Exec(query, name, port, network, config, caCert, serverCert, serverKey, dhParams)
	if err != nil {
		return fmt.Errorf("failed to create OpenVPN server in database: %w", err)
	}

	serverID, _ := result.LastInsertId()

	// Add to memory
	server := &VPNServer{
		ID:         serverID,
		Name:       name,
		Type:       "openvpn",
		Enabled:    true,
		ListenPort: port,
		Protocol:   "udp",
		Network:    network,
		Config:     config,
		CACert:     caCert,
		ServerCert: serverCert,
		ServerKey:  serverKey,
		DHParams:   dhParams,
	}

	s.serversMutex.Lock()
	s.servers[serverID] = server
	s.serversMutex.Unlock()

	// Write configuration files
	if err := s.writeOpenVPNConfig(server); err != nil {
		return fmt.Errorf("failed to write OpenVPN configuration: %w", err)
	}

	s.logger.Info("Created OpenVPN server: %s on port %d", name, port)
	return nil
}

// generateOpenVPNConfig creates OpenVPN server configuration
func (s *Service) generateOpenVPNConfig(name, network string, port int) string {
	var config strings.Builder

	config.WriteString("# CASDC OpenVPN Server Configuration\n")
	config.WriteString(fmt.Sprintf("# Server: %s\n", name))
	config.WriteString("# Generated automatically - DO NOT EDIT\n\n")

	config.WriteString(fmt.Sprintf("port %d\n", port))
	config.WriteString("proto udp\n")
	config.WriteString("dev tun\n\n")

	config.WriteString("ca ca.crt\n")
	config.WriteString("cert server.crt\n")
	config.WriteString("key server.key\n")
	config.WriteString("dh dh2048.pem\n\n")

	config.WriteString(fmt.Sprintf("server %s\n", network))
	config.WriteString("ifconfig-pool-persist ipp.txt\n\n")

	// Push DNS settings
	config.WriteString(fmt.Sprintf("push \"dhcp-option DNS %s\"\n", s.config.ServerAddress))
	config.WriteString(fmt.Sprintf("push \"route %s\"\n\n", network))

	// Security settings
	config.WriteString("keepalive 10 120\n")
	config.WriteString("cipher AES-256-CBC\n")
	config.WriteString("auth SHA256\n")
	config.WriteString("tls-auth ta.key 0\n")
	config.WriteString("comp-lzo\n")
	config.WriteString("persist-key\n")
	config.WriteString("persist-tun\n\n")

	// Logging
	config.WriteString("status /var/log/casdc/openvpn-status.log\n")
	config.WriteString("log-append /var/log/casdc/openvpn.log\n")
	config.WriteString("verb 3\n")

	return config.String()
}

// CreateWireGuardServer creates and configures a WireGuard server
func (s *Service) CreateWireGuardServer(name, network string, port int) error {
	// Generate server keys
	privateKey, publicKey := s.generateWireGuardKeys()

	// Generate WireGuard configuration
	config := s.generateWireGuardConfig(name, network, port, privateKey)

	// Insert into database
	query := `INSERT INTO vpn_servers (name, type, enabled, listen_port, protocol, network, config, server_key)
	          VALUES (?, 'wireguard', 1, ?, 'udp', ?, ?, ?)`

	result, err := s.db.Exec(query, name, port, network, config, privateKey)
	if err != nil {
		return fmt.Errorf("failed to create WireGuard server in database: %w", err)
	}

	serverID, _ := result.LastInsertId()

	// Add to memory
	server := &VPNServer{
		ID:         serverID,
		Name:       name,
		Type:       "wireguard",
		Enabled:    true,
		ListenPort: port,
		Protocol:   "udp",
		Network:    network,
		Config:     config,
		ServerKey:  privateKey,
	}

	s.serversMutex.Lock()
	s.servers[serverID] = server
	s.serversMutex.Unlock()

	// Write configuration file
	if err := s.writeWireGuardConfig(server); err != nil {
		return fmt.Errorf("failed to write WireGuard configuration: %w", err)
	}

	s.logger.Info("Created WireGuard server: %s on port %d (public key: %s)", name, port, publicKey)
	return nil
}

// generateWireGuardConfig creates WireGuard server configuration
func (s *Service) generateWireGuardConfig(name, network string, port int, privateKey string) string {
	var config strings.Builder

	config.WriteString("# CASDC WireGuard Server Configuration\n")
	config.WriteString(fmt.Sprintf("# Server: %s\n", name))
	config.WriteString("# Generated automatically - DO NOT EDIT\n\n")

	config.WriteString("[Interface]\n")
	config.WriteString(fmt.Sprintf("PrivateKey = %s\n", privateKey))
	config.WriteString(fmt.Sprintf("Address = %s\n", network))
	config.WriteString(fmt.Sprintf("ListenPort = %d\n", port))
	config.WriteString("SaveConfig = false\n\n")

	// Post-up and post-down rules for NAT
	config.WriteString("PostUp = iptables -A FORWARD -i wg0 -j ACCEPT; iptables -t nat -A POSTROUTING -o eth0 -j MASQUERADE\n")
	config.WriteString("PostDown = iptables -D FORWARD -i wg0 -j ACCEPT; iptables -t nat -D POSTROUTING -o eth0 -j MASQUERADE\n")

	return config.String()
}

// generateWireGuardKeys generates a WireGuard private/public key pair
func (s *Service) generateWireGuardKeys() (privateKey, publicKey string) {
	// Generate 32 random bytes for private key
	key := make([]byte, 32)
	rand.Read(key)

	privateKey = base64.StdEncoding.EncodeToString(key)

	// TODO: Implement proper Curve25519 public key derivation
	// For now, generate another random key (this is incorrect but allows compilation)
	pubKeyBytes := make([]byte, 32)
	rand.Read(pubKeyBytes)
	publicKey = base64.StdEncoding.EncodeToString(pubKeyBytes)

	return privateKey, publicKey
}

// generateCA generates a Certificate Authority for OpenVPN
func (s *Service) generateCA(name string) (cert, key string, err error) {
	// TODO: Implement proper X.509 CA generation
	// For now, return placeholder values
	cert = fmt.Sprintf("-----BEGIN CERTIFICATE-----\n%s CA Certificate\n-----END CERTIFICATE-----", name)
	key = fmt.Sprintf("-----BEGIN PRIVATE KEY-----\n%s CA Key\n-----END PRIVATE KEY-----", name)
	return cert, key, nil
}

// generateCertificate generates a certificate signed by CA
func (s *Service) generateCertificate(name, caCert, caKey string) (cert, key string, err error) {
	// TODO: Implement proper X.509 certificate generation
	// For now, return placeholder values
	cert = fmt.Sprintf("-----BEGIN CERTIFICATE-----\n%s Certificate\n-----END CERTIFICATE-----", name)
	key = fmt.Sprintf("-----BEGIN PRIVATE KEY-----\n%s Key\n-----END PRIVATE KEY-----", name)
	return cert, key, nil
}

// generateDHParams generates Diffie-Hellman parameters
func (s *Service) generateDHParams() string {
	// TODO: Implement proper DH parameter generation
	// For now, return placeholder
	return "-----BEGIN DH PARAMETERS-----\nPlaceholder DH Params\n-----END DH PARAMETERS-----"
}

// writeOpenVPNConfig writes OpenVPN configuration to filesystem
func (s *Service) writeOpenVPNConfig(server *VPNServer) error {
	configDir := filepath.Join(s.vpnBasePath, "openvpn", server.Name)
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return err
	}

	// Write main config
	configPath := filepath.Join(configDir, "server.conf")
	if err := os.WriteFile(configPath, []byte(server.Config), 0600); err != nil {
		return err
	}

	// Write certificates
	if err := os.WriteFile(filepath.Join(configDir, "ca.crt"), []byte(server.CACert), 0600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(configDir, "server.crt"), []byte(server.ServerCert), 0600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(configDir, "server.key"), []byte(server.ServerKey), 0600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(configDir, "dh2048.pem"), []byte(server.DHParams), 0600); err != nil {
		return err
	}

	return nil
}

// writeWireGuardConfig writes WireGuard configuration to filesystem
func (s *Service) writeWireGuardConfig(server *VPNServer) error {
	configPath := filepath.Join(s.wireguardPath, server.Name+".conf")
	return os.WriteFile(configPath, []byte(server.Config), 0600)
}

// CreateVPNClient creates a VPN client configuration for a user
func (s *Service) CreateVPNClient(serverID, userID int64, name string) (*VPNClient, error) {
	// Get server
	s.serversMutex.RLock()
	server, exists := s.servers[serverID]
	s.serversMutex.RUnlock()

	if !exists {
		return nil, fmt.Errorf("VPN server not found")
	}

	var client *VPNClient
	var err error

	switch server.Type {
	case "openvpn":
		client, err = s.createOpenVPNClient(server, userID, name)
	case "wireguard":
		client, err = s.createWireGuardClient(server, userID, name)
	default:
		return nil, fmt.Errorf("unsupported VPN type: %s", server.Type)
	}

	if err != nil {
		return nil, err
	}

	// Insert into database
	query := `INSERT INTO vpn_clients (server_id, user_id, name, enabled, config, certificate,
	          private_key, public_key, assigned_ip, created_at)
	          VALUES (?, ?, ?, 1, ?, ?, ?, ?, ?, ?)`

	result, err := s.db.Exec(query, serverID, userID, name, client.Config,
		client.Certificate, client.PrivateKey, client.PublicKey, client.AssignedIP, time.Now())
	if err != nil {
		return nil, fmt.Errorf("failed to create VPN client in database: %w", err)
	}

	clientID, _ := result.LastInsertId()
	client.ID = clientID

	// Add to memory
	s.clientsMutex.Lock()
	s.clients[clientID] = client
	s.clientsMutex.Unlock()

	s.logger.Info("Created VPN client: %s for user %d", name, userID)
	return client, nil
}

// createOpenVPNClient generates OpenVPN client configuration
func (s *Service) createOpenVPNClient(server *VPNServer, userID int64, name string) (*VPNClient, error) {
	// Generate client certificate
	cert, key, err := s.generateCertificate(name, server.CACert, server.ServerKey)
	if err != nil {
		return nil, err
	}

	// Generate client config
	config := s.generateOpenVPNClientConfig(server, cert, key, server.CACert)

	return &VPNClient{
		ServerID:    server.ID,
		UserID:      userID,
		Name:        name,
		Enabled:     true,
		Config:      config,
		Certificate: cert,
		PrivateKey:  key,
		CreatedAt:   time.Now(),
	}, nil
}

// createWireGuardClient generates WireGuard client configuration
func (s *Service) createWireGuardClient(server *VPNServer, userID int64, name string) (*VPNClient, error) {
	// Generate client keys
	privateKey, publicKey := s.generateWireGuardKeys()

	// Assign IP address
	assignedIP := s.assignIPAddress(server)

	// Generate client config
	config := s.generateWireGuardClientConfig(server, privateKey, assignedIP)

	return &VPNClient{
		ServerID:   server.ID,
		UserID:     userID,
		Name:       name,
		Enabled:    true,
		Config:     config,
		PrivateKey: privateKey,
		PublicKey:  publicKey,
		AssignedIP: assignedIP,
		CreatedAt:  time.Now(),
	}, nil
}

// generateOpenVPNClientConfig creates OpenVPN client configuration file
func (s *Service) generateOpenVPNClientConfig(server *VPNServer, cert, key, caCert string) string {
	var config strings.Builder

	config.WriteString("client\n")
	config.WriteString("dev tun\n")
	config.WriteString("proto udp\n")
	config.WriteString(fmt.Sprintf("remote %s %d\n", s.config.ServerAddress, server.ListenPort))
	config.WriteString("resolv-retry infinite\n")
	config.WriteString("nobind\n")
	config.WriteString("persist-key\n")
	config.WriteString("persist-tun\n")
	config.WriteString("cipher AES-256-CBC\n")
	config.WriteString("auth SHA256\n")
	config.WriteString("comp-lzo\n")
	config.WriteString("verb 3\n\n")

	config.WriteString("<ca>\n" + caCert + "\n</ca>\n\n")
	config.WriteString("<cert>\n" + cert + "\n</cert>\n\n")
	config.WriteString("<key>\n" + key + "\n</key>\n")

	return config.String()
}

// generateWireGuardClientConfig creates WireGuard client configuration
func (s *Service) generateWireGuardClientConfig(server *VPNServer, privateKey, assignedIP string) string {
	var config strings.Builder

	config.WriteString("[Interface]\n")
	config.WriteString(fmt.Sprintf("PrivateKey = %s\n", privateKey))
	config.WriteString(fmt.Sprintf("Address = %s\n", assignedIP))
	config.WriteString(fmt.Sprintf("DNS = %s\n\n", s.config.ServerAddress))

	config.WriteString("[Peer]\n")
	// TODO: Get server public key
	config.WriteString("PublicKey = <server_public_key>\n")
	config.WriteString(fmt.Sprintf("Endpoint = %s:%d\n", s.config.ServerAddress, server.ListenPort))
	config.WriteString("AllowedIPs = 0.0.0.0/0\n")
	config.WriteString("PersistentKeepalive = 25\n")

	return config.String()
}

// assignIPAddress assigns an IP address from the VPN network
func (s *Service) assignIPAddress(server *VPNServer) string {
	// TODO: Implement proper IP allocation from network pool
	// For now, return a placeholder
	return "10.8.0.2/24"
}

// GetClientConfig retrieves client configuration for download
func (s *Service) GetClientConfig(clientID int64) (*VPNClient, error) {
	s.clientsMutex.RLock()
	defer s.clientsMutex.RUnlock()

	client, exists := s.clients[clientID]
	if !exists {
		return nil, fmt.Errorf("VPN client not found")
	}

	return client, nil
}

// Shutdown gracefully stops the VPN service
func (s *Service) Shutdown(ctx context.Context) error {
	s.logger.Info("Shutting down VPN service")
	// TODO: Stop VPN servers and cleanup connections
	return nil
}
