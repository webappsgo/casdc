// Package dns provides DNS services for CASDC with complete BIND integration
// Supporting authoritative DNS, recursive resolver, and dynamic updates
package dns

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/casapps/casdc/internal/config"
	"github.com/casapps/casdc/internal/database"
	"github.com/casapps/casdc/pkg/logger"
)

// Service represents the DNS service with complete BIND integration
type Service struct {
	db     *database.DB
	config *config.Config
	logger *logger.Logger

	// BIND configuration paths (auto-detected)
	bindConfDir  string
	bindDataDir  string
	serviceName  string

	// Zone management
	zones    map[string]*Zone
	zonesMux sync.RWMutex

	// BIND process management
	bindProcess *os.Process
	isRunning   bool
	mu          sync.Mutex
}

// Zone represents a DNS zone configuration
type Zone struct {
	ID          int64
	Name        string
	Type        string  // forward, reverse
	Class       string  // IN
	TTL         int64
	Serial      int64
	Refresh     int64
	Retry       int64
	Expire      int64
	Minimum     int64
	PrimaryNS   string
	AdminEmail  string
	Enabled     bool
	DNSSECEnabled bool
	Records     []*Record
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Record represents a DNS resource record
type Record struct {
	ID       int64
	ZoneID   int64
	Name     string
	Type     string  // A, AAAA, CNAME, MX, TXT, SRV, PTR, NS, SOA
	Value    string
	TTL      int64
	Priority int64
	Weight   int64
	Port     int64
	Enabled  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// BINDConfig represents the complete BIND configuration structure
type BINDConfig struct {
	ConfigDir   string
	DataDir     string
	ServiceName string
	Zones       []*Zone
	ACLs        []ACL
	Views       []View
	Options     BINDOptions
	Logging     BINDLogging
}

// ACL represents BIND access control list
type ACL struct {
	Name    string
	Entries []string
}

// View represents BIND view configuration
type View struct {
	Name      string
	Match     string
	Recursion bool
	Zones     []*Zone
}

// BINDOptions represents global BIND options
type BINDOptions struct {
	ListenOn          []string
	ListenOnV6        []string
	Directory         string
	DNSSECEnable      bool
	DNSSECValidation  string
	Recursion         bool
	AllowQuery        []string
	AllowRecursion    []string
	AllowTransfer     []string
	ForwardOnly       bool
	Forwarders        []string
	QueryLog          bool
	Version           string
}

// BINDLogging represents BIND logging configuration
type BINDLogging struct {
	DefaultFile   string
	QueriesFile   string
	SecurityFile  string
	UpdateFile    string
	XferInFile    string
	XferOutFile   string
	Severity      string
}

// NewService creates a new DNS service with BIND integration
func NewService(db *database.DB, cfg *config.Config, log *logger.Logger) (*Service, error) {
	s := &Service{
		db:     db,
		config: cfg,
		logger: log,
		zones:  make(map[string]*Zone),
	}

	// Auto-detect BIND configuration directories
	if err := s.detectBINDPaths(); err != nil {
		return nil, fmt.Errorf("failed to detect BIND paths: %w", err)
	}

	// Load existing zones from database
	if err := s.loadZones(); err != nil {
		return nil, fmt.Errorf("failed to load zones: %w", err)
	}

	// Take control of BIND configuration
	if err := s.takeBINDControl(); err != nil {
		return nil, fmt.Errorf("failed to take BIND control: %w", err)
	}

	// Generate initial BIND configuration
	if err := s.generateBINDConfig(); err != nil {
		return nil, fmt.Errorf("failed to generate BIND config: %w", err)
	}

	// Start BIND service
	if err := s.startBIND(); err != nil {
		return nil, fmt.Errorf("failed to start BIND: %w", err)
	}

	// Create default zones if none exist
	if len(s.zones) == 0 {
		if err := s.createDefaultZones(); err != nil {
			log.Warn("Failed to create default zones: %v", err)
		}
	}

	return s, nil
}

// detectBINDPaths auto-detects BIND configuration based on distribution
func (s *Service) detectBINDPaths() error {
	// Common BIND configuration paths by distribution
	bindPaths := []struct {
		confDir     string
		dataDir     string
		serviceName string
	}{
		{"/etc/bind", "/var/lib/bind", "bind9"},         // Debian/Ubuntu
		{"/etc/bind", "/var/cache/bind", "bind9"},       // Ubuntu alternative
		{"/etc/named", "/var/named", "named"},           // RHEL/CentOS
		{"/etc/named", "/var/named", "named-chroot"},    // RHEL/CentOS with chroot
		{"/usr/local/etc/named", "/var/named", "named"}, // FreeBSD
		{"/etc/bind", "/var/lib/named", "named"},        // openSUSE
	}

	for _, path := range bindPaths {
		if _, err := os.Stat(path.confDir); err == nil {
			s.bindConfDir = path.confDir
			s.bindDataDir = path.dataDir
			s.serviceName = path.serviceName

			s.logger.Info("Detected BIND configuration: %s (service: %s)",
				s.bindConfDir, s.serviceName)
			return nil
		}
	}

	// Default to common path if none found
	s.bindConfDir = "/etc/bind"
	s.bindDataDir = "/var/lib/bind"
	s.serviceName = "bind9"

	s.logger.Warn("BIND not detected, using defaults: %s", s.bindConfDir)
	return nil
}

// takeBINDControl takes control of BIND configuration
func (s *Service) takeBINDControl() error {
	s.logger.Info("Taking control of BIND configuration")

	// Create CASDC configuration directories
	casdcConfDir := filepath.Join(s.bindConfDir, "casdc")
	directories := []string{
		casdcConfDir,
		filepath.Join(casdcConfDir, "keys"),
		filepath.Join(s.bindDataDir, "casdc", "primary"),
		filepath.Join(s.bindDataDir, "casdc", "secondary"),
		filepath.Join(s.bindDataDir, "casdc", "dynamic"),
		filepath.Join(s.bindDataDir, "casdc", "local"),
		filepath.Join(s.bindDataDir, "casdc", "dnssec"),
		filepath.Join(s.bindDataDir, "casdc", "cache"),
	}

	for _, dir := range directories {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	// Backup original named.conf if it exists
	originalConf := filepath.Join(s.bindConfDir, "named.conf")
	backupConf := filepath.Join(s.bindConfDir, "named.conf.casdc-backup")

	if _, err := os.Stat(originalConf); err == nil {
		if _, err := os.Stat(backupConf); os.IsNotExist(err) {
			if err := s.copyFile(originalConf, backupConf); err != nil {
				s.logger.Warn("Failed to backup original named.conf: %v", err)
			} else {
				s.logger.Info("Backed up original named.conf to named.conf.casdc-backup")
			}
		}
	}

	return nil
}

// generateBINDConfig generates complete BIND configuration from database
func (s *Service) generateBINDConfig() error {
	s.logger.Debug("Generating BIND configuration from database")

	config := &BINDConfig{
		ConfigDir:   s.bindConfDir,
		DataDir:     s.bindDataDir,
		ServiceName: s.serviceName,
		Zones:       make([]*Zone, 0),
		ACLs: []ACL{
			{
				Name:    "trusted",
				Entries: []string{"localhost", "localnets"},
			},
			{
				Name:    "casdc_internal",
				Entries: []string{"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"},
			},
		},
		Options: BINDOptions{
			ListenOn:         []string{"any"},
			ListenOnV6:       []string{"any"},
			Directory:        s.bindDataDir,
			DNSSECEnable:     true,
			DNSSECValidation: "auto",
			Recursion:        true,
			AllowQuery:       []string{"casdc_internal"},
			AllowRecursion:   []string{"casdc_internal"},
			AllowTransfer:    []string{"none"},
			ForwardOnly:      false,
			Forwarders:       []string{"8.8.8.8", "8.8.4.4", "1.1.1.1", "1.0.0.1"},
			QueryLog:         false,
			Version:          "CASDC DNS Server",
		},
		Logging: BINDLogging{
			DefaultFile:  "/var/log/casdc/bind.log",
			QueriesFile:  "/var/log/casdc/bind-queries.log",
			SecurityFile: "/var/log/casdc/bind-security.log",
			UpdateFile:   "/var/log/casdc/bind-updates.log",
			XferInFile:   "/var/log/casdc/bind-xfer-in.log",
			XferOutFile:  "/var/log/casdc/bind-xfer-out.log",
			Severity:     "info",
		},
	}

	// Add zones from memory
	s.zonesMux.RLock()
	for _, zone := range s.zones {
		if zone.Enabled {
			config.Zones = append(config.Zones, zone)
		}
	}
	s.zonesMux.RUnlock()

	// Generate main named.conf
	if err := s.generateNamedConf(config); err != nil {
		return fmt.Errorf("failed to generate named.conf: %w", err)
	}

	// Generate zones.conf
	if err := s.generateZonesConf(config); err != nil {
		return fmt.Errorf("failed to generate zones.conf: %w", err)
	}

	// Generate individual zone files
	for _, zone := range config.Zones {
		if err := s.generateZoneFile(zone); err != nil {
			s.logger.Warn("Failed to generate zone file for %s: %v", zone.Name, err)
		}
	}

	// Generate CASDC-specific configuration files
	if err := s.generateCasdcConfigs(config); err != nil {
		return fmt.Errorf("failed to generate CASDC configs: %w", err)
	}

	return nil
}

// generateNamedConf generates the main named.conf file
func (s *Service) generateNamedConf(config *BINDConfig) error {
	tmplStr := `// CASDC-managed BIND configuration
// This file is automatically generated - do not edit manually

include "{{.ConfigDir}}/casdc/acl.conf";
include "{{.ConfigDir}}/casdc/logging.conf";
include "{{.ConfigDir}}/casdc/options.conf";
include "{{.ConfigDir}}/casdc/views.conf";
include "{{.ConfigDir}}/zones.conf";

// Include system-wide configuration if available
include "/etc/bind/named.conf.local";
include "/etc/bind/named.conf.default-zones";
`

	tmpl, err := template.New("named.conf").Parse(tmplStr)
	if err != nil {
		return fmt.Errorf("failed to parse named.conf template: %w", err)
	}

	confPath := filepath.Join(s.bindConfDir, "named.conf")
	file, err := os.Create(confPath)
	if err != nil {
		return fmt.Errorf("failed to create named.conf: %w", err)
	}
	defer file.Close()

	if err := tmpl.Execute(file, config); err != nil {
		return fmt.Errorf("failed to execute named.conf template: %w", err)
	}

	s.logger.Debug("Generated named.conf at %s", confPath)
	return nil
}

// generateZonesConf generates zones configuration file
func (s *Service) generateZonesConf(config *BINDConfig) error {
	tmplStr := `// CASDC-managed zone definitions
// This file is automatically generated from the database

{{range .Zones}}
zone "{{.Name}}" IN {
    type master;
    file "{{$.DataDir}}/casdc/primary/{{.Name}}.zone";
    {{if .DNSSECEnabled}}
    key-directory "{{$.DataDir}}/casdc/dnssec/{{.Name}}";
    auto-dnssec maintain;
    inline-signing yes;
    {{end}}
    allow-update { none; };
    allow-transfer { none; };
    allow-query { casdc_internal; };
    notify yes;
};

{{end}}
`

	tmpl, err := template.New("zones.conf").Parse(tmplStr)
	if err != nil {
		return fmt.Errorf("failed to parse zones.conf template: %w", err)
	}

	confPath := filepath.Join(s.bindConfDir, "zones.conf")
	file, err := os.Create(confPath)
	if err != nil {
		return fmt.Errorf("failed to create zones.conf: %w", err)
	}
	defer file.Close()

	if err := tmpl.Execute(file, config); err != nil {
		return fmt.Errorf("failed to execute zones.conf template: %w", err)
	}

	s.logger.Debug("Generated zones.conf with %d zones", len(config.Zones))
	return nil
}

// generateZoneFile generates individual zone files
func (s *Service) generateZoneFile(zone *Zone) error {
	// Increment serial number for updates
	zone.Serial = time.Now().Unix()

	tmplStr := `; CASDC-managed zone file for {{.Name}}
; This file is automatically generated from the database
; Serial: {{.Serial}}

$ORIGIN {{.Name}}.
$TTL {{.TTL}}

@ IN SOA {{.PrimaryNS}}. {{.AdminEmail}}. (
    {{.Serial}}    ; serial
    {{.Refresh}}   ; refresh
    {{.Retry}}     ; retry
    {{.Expire}}    ; expire
    {{.Minimum}}   ; minimum TTL
)

{{range .Records}}
{{if .Enabled}}
{{.Name}} {{.TTL}} IN {{.Type}} {{if eq .Type "MX"}}{{.Priority}} {{end}}{{if eq .Type "SRV"}}{{.Priority}} {{.Weight}} {{.Port}} {{end}}{{.Value}}
{{end}}
{{end}}
`

	tmpl, err := template.New("zone").Parse(tmplStr)
	if err != nil {
		return fmt.Errorf("failed to parse zone template: %w", err)
	}

	zonePath := filepath.Join(s.bindDataDir, "casdc", "primary", zone.Name+".zone")
	file, err := os.Create(zonePath)
	if err != nil {
		return fmt.Errorf("failed to create zone file: %w", err)
	}
	defer file.Close()

	if err := tmpl.Execute(file, zone); err != nil {
		return fmt.Errorf("failed to execute zone template: %w", err)
	}

	s.logger.Debug("Generated zone file for %s at %s", zone.Name, zonePath)
	return nil
}

// generateCasdcConfigs generates CASDC-specific BIND configuration files
func (s *Service) generateCasdcConfigs(config *BINDConfig) error {
	casdcDir := filepath.Join(s.bindConfDir, "casdc")

	// Generate ACL configuration
	aclContent := `// CASDC Access Control Lists
acl "trusted" {
    localhost;
    localnets;
};

acl "casdc_internal" {
    127.0.0.0/8;
    10.0.0.0/8;
    172.16.0.0/12;
    192.168.0.0/16;
};
`
	if err := s.writeFile(filepath.Join(casdcDir, "acl.conf"), aclContent); err != nil {
		return err
	}

	// Generate options configuration
	optionsContent := fmt.Sprintf(`// CASDC Global Options
options {
    directory "%s";
    listen-on port 53 { any; };
    listen-on-v6 port 53 { any; };

    recursion yes;
    allow-query { casdc_internal; };
    allow-recursion { casdc_internal; };
    allow-transfer { none; };

    dnssec-enable yes;
    dnssec-validation auto;

    auth-nxdomain no;

    version "%s";
    hostname none;
    server-id none;

    // Forward to public DNS servers
    forwarders {
        8.8.8.8;
        8.8.4.4;
        1.1.1.1;
        1.0.0.1;
    };
};
`, s.bindDataDir, config.Options.Version)

	if err := s.writeFile(filepath.Join(casdcDir, "options.conf"), optionsContent); err != nil {
		return err
	}

	// Generate logging configuration
	loggingContent := `// CASDC Logging Configuration
logging {
    channel default_log {
        file "/var/log/casdc/bind.log" versions 3 size 5m;
        severity info;
        print-category yes;
        print-severity yes;
        print-time yes;
    };

    channel query_log {
        file "/var/log/casdc/bind-queries.log" versions 3 size 10m;
        severity info;
        print-time yes;
    };

    channel security_log {
        file "/var/log/casdc/bind-security.log" versions 3 size 5m;
        severity info;
        print-time yes;
    };

    category default { default_log; };
    category general { default_log; };
    category queries { query_log; };
    category security { security_log; };
    category config { default_log; };
    category resolver { default_log; };
    category xfer-in { default_log; };
    category xfer-out { default_log; };
    category notify { default_log; };
    category client { default_log; };
    category network { default_log; };
    category update { default_log; };
    category update-security { security_log; };
    category lame-servers { null; };
};
`
	if err := s.writeFile(filepath.Join(casdcDir, "logging.conf"), loggingContent); err != nil {
		return err
	}

	// Generate views configuration (empty for now)
	viewsContent := `// CASDC Views Configuration
// Views will be added here for split-horizon DNS
`
	if err := s.writeFile(filepath.Join(casdcDir, "views.conf"), viewsContent); err != nil {
		return err
	}

	return nil
}

// loadZones loads zones from database
func (s *Service) loadZones() error {
	rows, err := s.db.Query(`
		SELECT id, name, type, class, ttl, serial, refresh, retry, expire, minimum,
		       primary_ns, admin_email, enabled, dnssec_enabled, created_at, updated_at
		FROM dns_zones
		ORDER BY name`)
	if err != nil {
		return fmt.Errorf("failed to query zones: %w", err)
	}
	defer rows.Close()

	s.zonesMux.Lock()
	defer s.zonesMux.Unlock()

	for rows.Next() {
		zone := &Zone{}
		if err := rows.Scan(
			&zone.ID, &zone.Name, &zone.Type, &zone.Class, &zone.TTL,
			&zone.Serial, &zone.Refresh, &zone.Retry, &zone.Expire, &zone.Minimum,
			&zone.PrimaryNS, &zone.AdminEmail, &zone.Enabled, &zone.DNSSECEnabled,
			&zone.CreatedAt, &zone.UpdatedAt,
		); err != nil {
			s.logger.Warn("Failed to scan zone: %v", err)
			continue
		}

		// Load records for this zone
		if err := s.loadZoneRecords(zone); err != nil {
			s.logger.Warn("Failed to load records for zone %s: %v", zone.Name, err)
		}

		s.zones[zone.Name] = zone
	}

	s.logger.Info("Loaded %d zones from database", len(s.zones))
	return nil
}

// loadZoneRecords loads DNS records for a specific zone
func (s *Service) loadZoneRecords(zone *Zone) error {
	rows, err := s.db.Query(`
		SELECT id, name, type, value, ttl, priority, weight, port, enabled, created_at, updated_at
		FROM dns_records
		WHERE zone_id = ? AND enabled = TRUE
		ORDER BY type, name`, zone.ID)
	if err != nil {
		return fmt.Errorf("failed to query records: %w", err)
	}
	defer rows.Close()

	zone.Records = make([]*Record, 0)
	for rows.Next() {
		record := &Record{ZoneID: zone.ID}
		if err := rows.Scan(
			&record.ID, &record.Name, &record.Type, &record.Value,
			&record.TTL, &record.Priority, &record.Weight, &record.Port,
			&record.Enabled, &record.CreatedAt, &record.UpdatedAt,
		); err != nil {
			s.logger.Warn("Failed to scan record: %v", err)
			continue
		}
		zone.Records = append(zone.Records, record)
	}

	return nil
}

// createDefaultZones creates default DNS zones for the domain
func (s *Service) createDefaultZones() error {
	domain := s.config.Domain
	if domain == "" {
		domain = "example.local"
	}

	// Create forward zone
	forwardZone := &Zone{
		Name:        domain,
		Type:        "forward",
		Class:       "IN",
		TTL:         86400,
		Serial:      time.Now().Unix(),
		Refresh:     3600,
		Retry:       1800,
		Expire:      604800,
		Minimum:     86400,
		PrimaryNS:   fmt.Sprintf("casdc.%s", domain),
		AdminEmail:  strings.Replace(s.config.AdminEmail, "@", ".", 1),
		Enabled:     true,
		DNSSECEnabled: false,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	// Add default records
	forwardZone.Records = []*Record{
		{
			Name:    "@",
			Type:    "NS",
			Value:   fmt.Sprintf("casdc.%s.", domain),
			TTL:     86400,
			Enabled: true,
		},
		{
			Name:    "casdc",
			Type:    "A",
			Value:   s.config.ServerAddress,
			TTL:     300,
			Enabled: true,
		},
		{
			Name:    "@",
			Type:    "A",
			Value:   s.config.ServerAddress,
			TTL:     300,
			Enabled: true,
		},
		{
			Name:    "www",
			Type:    "CNAME",
			Value:   "@",
			TTL:     300,
			Enabled: true,
		},
		{
			Name:    "mail",
			Type:    "A",
			Value:   s.config.ServerAddress,
			TTL:     300,
			Enabled: true,
		},
		{
			Name:    "@",
			Type:    "MX",
			Value:   fmt.Sprintf("mail.%s.", domain),
			TTL:     300,
			Priority: 10,
			Enabled: true,
		},
	}

	// Save to database and add to memory
	if err := s.saveZone(forwardZone); err != nil {
		return fmt.Errorf("failed to save forward zone: %w", err)
	}

	s.zonesMux.Lock()
	s.zones[forwardZone.Name] = forwardZone
	s.zonesMux.Unlock()

	s.logger.Info("Created default forward zone for %s", domain)

	// Automatically create Active Directory SRV records for Windows domain join support
	if err := s.CreateActiveDirectorySRVRecords(domain); err != nil {
		s.logger.Warn("Failed to create Active Directory SRV records: %v", err)
	}

	return nil
}

// saveZone saves a zone to the database
func (s *Service) saveZone(zone *Zone) error {
	// Insert zone
	result, err := s.db.Exec(`
		INSERT INTO dns_zones (name, type, class, ttl, serial, refresh, retry, expire, minimum,
		                      primary_ns, admin_email, enabled, dnssec_enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		zone.Name, zone.Type, zone.Class, zone.TTL, zone.Serial,
		zone.Refresh, zone.Retry, zone.Expire, zone.Minimum,
		zone.PrimaryNS, zone.AdminEmail, zone.Enabled, zone.DNSSECEnabled,
		zone.CreatedAt, zone.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to insert zone: %w", err)
	}

	zoneID, _ := result.LastInsertId()
	zone.ID = zoneID

	// Insert records
	for _, record := range zone.Records {
		record.ZoneID = zoneID
		if err := s.saveRecord(record); err != nil {
			s.logger.Warn("Failed to save record %s: %v", record.Name, err)
		}
	}

	return nil
}

// saveRecord saves a DNS record to the database
func (s *Service) saveRecord(record *Record) error {
	result, err := s.db.Exec(`
		INSERT INTO dns_records (zone_id, name, type, value, ttl, priority, weight, port, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ZoneID, record.Name, record.Type, record.Value, record.TTL,
		record.Priority, record.Weight, record.Port, record.Enabled,
		time.Now(), time.Now(),
	)
	if err != nil {
		return fmt.Errorf("failed to insert record: %w", err)
	}

	recordID, _ := result.LastInsertId()
	record.ID = recordID
	return nil
}

// startBIND starts the BIND service
func (s *Service) startBIND() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.isRunning {
		return nil
	}

	// Check BIND configuration
	if err := s.checkBINDConfig(); err != nil {
		return fmt.Errorf("BIND configuration check failed: %w", err)
	}

	// Start BIND using systemctl
	cmd := exec.Command("systemctl", "start", s.serviceName)
	if err := cmd.Run(); err != nil {
		// Try alternative start methods
		if err := s.startBINDAlternative(); err != nil {
			return fmt.Errorf("failed to start BIND service: %w", err)
		}
	}

	// Verify BIND is running
	if err := s.waitForBINDStart(); err != nil {
		return fmt.Errorf("BIND failed to start: %w", err)
	}

	s.isRunning = true
	s.logger.Info("BIND DNS service started successfully")
	return nil
}

// checkBINDConfig validates BIND configuration
func (s *Service) checkBINDConfig() error {
	cmd := exec.Command("named-checkconf", filepath.Join(s.bindConfDir, "named.conf"))
	if output, err := cmd.CombinedOutput(); err != nil {
		s.logger.Error("BIND configuration check failed: %s", string(output))
		return fmt.Errorf("configuration check failed: %w", err)
	}
	return nil
}

// startBINDAlternative tries alternative methods to start BIND
func (s *Service) startBINDAlternative() error {
	// Try direct named command
	cmd := exec.Command("named", "-c", filepath.Join(s.bindConfDir, "named.conf"))
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start named directly: %w", err)
	}

	s.bindProcess = cmd.Process
	return nil
}

// waitForBINDStart waits for BIND to become responsive
func (s *Service) waitForBINDStart() error {
	for i := 0; i < 10; i++ {
		// Test DNS resolution
		if s.testDNSQuery() {
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("BIND did not become responsive")
}

// testDNSQuery tests if DNS is responding
func (s *Service) testDNSQuery() bool {
	_, err := net.LookupHost("localhost")
	return err == nil
}

// reloadBIND reloads BIND configuration
func (s *Service) reloadBIND() error {
	cmd := exec.Command("systemctl", "reload", s.serviceName)
	if err := cmd.Run(); err != nil {
		// Try rndc reload
		cmd = exec.Command("rndc", "reload")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to reload BIND: %w", err)
		}
	}

	s.logger.Info("BIND configuration reloaded")
	return nil
}

// Shutdown gracefully stops the DNS service
func (s *Service) Shutdown(ctx context.Context) error {
	s.logger.Info("Shutting down DNS service")

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.isRunning {
		return nil
	}

	// Stop BIND service
	cmd := exec.Command("systemctl", "stop", s.serviceName)
	if err := cmd.Run(); err != nil {
		s.logger.Warn("Failed to stop BIND via systemctl: %v", err)

		// Try killing the process directly
		if s.bindProcess != nil {
			if err := s.bindProcess.Kill(); err != nil {
				s.logger.Warn("Failed to kill BIND process: %v", err)
			}
		}
	}

	s.isRunning = false
	return nil
}

// Helper functions

// copyFile copies a file from src to dst
func (s *Service) copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = sourceFile.WriteTo(destFile)
	return err
}

// writeFile writes content to a file
func (s *Service) writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}

// Database helper methods are accessed directly via s.db

// CreateActiveDirectorySRVRecords automatically generates AD SRV records for Windows domain join
// This is critical for Windows clients to discover domain controllers and services
func (s *Service) CreateActiveDirectorySRVRecords(domain string) error {
	s.logger.Info("Creating Active Directory SRV records for domain %s", domain)

	// Get the zone for this domain
	s.zonesMux.RLock()
	zone, exists := s.zones[domain]
	s.zonesMux.RUnlock()

	if !exists {
		return fmt.Errorf("zone %s does not exist", domain)
	}

	// Standard Active Directory SRV records per Microsoft specifications
	// These records are essential for Windows domain join and functionality
	srvRecords := []struct {
		name     string
		priority int64
		weight   int64
		port     int64
		target   string
		description string
	}{
		// LDAP service records
		{
			name:     "_ldap._tcp",
			priority: 0,
			weight:   100,
			port:     389,
			target:   fmt.Sprintf("casdc.%s.", domain),
			description: "LDAP service for domain controller location",
		},
		{
			name:     "_ldap._tcp.dc._msdcs",
			priority: 0,
			weight:   100,
			port:     389,
			target:   fmt.Sprintf("casdc.%s.", domain),
			description: "LDAP service for domain controller (Microsoft specific)",
		},
		{
			name:     "_ldap._tcp.pdc._msdcs",
			priority: 0,
			weight:   100,
			port:     389,
			target:   fmt.Sprintf("casdc.%s.", domain),
			description: "LDAP service for primary domain controller",
		},
		{
			name:     "_ldap._tcp.gc._msdcs",
			priority: 0,
			weight:   100,
			port:     3268,
			target:   fmt.Sprintf("casdc.%s.", domain),
			description: "LDAP Global Catalog service",
		},

		// LDAPS (LDAP over SSL) service records
		{
			name:     "_ldaps._tcp",
			priority: 0,
			weight:   100,
			port:     636,
			target:   fmt.Sprintf("casdc.%s.", domain),
			description: "LDAPS (LDAP over SSL) service",
		},
		{
			name:     "_ldaps._tcp.dc._msdcs",
			priority: 0,
			weight:   100,
			port:     636,
			target:   fmt.Sprintf("casdc.%s.", domain),
			description: "LDAPS service for domain controller",
		},
		{
			name:     "_ldaps._tcp.gc._msdcs",
			priority: 0,
			weight:   100,
			port:     3269,
			target:   fmt.Sprintf("casdc.%s.", domain),
			description: "LDAPS Global Catalog service",
		},

		// Kerberos service records
		{
			name:     "_kerberos._tcp",
			priority: 0,
			weight:   100,
			port:     88,
			target:   fmt.Sprintf("casdc.%s.", domain),
			description: "Kerberos authentication service",
		},
		{
			name:     "_kerberos._tcp.dc._msdcs",
			priority: 0,
			weight:   100,
			port:     88,
			target:   fmt.Sprintf("casdc.%s.", domain),
			description: "Kerberos authentication for domain controller",
		},
		{
			name:     "_kerberos._udp",
			priority: 0,
			weight:   100,
			port:     88,
			target:   fmt.Sprintf("casdc.%s.", domain),
			description: "Kerberos authentication service (UDP)",
		},
		{
			name:     "_kerberos._udp.dc._msdcs",
			priority: 0,
			weight:   100,
			port:     88,
			target:   fmt.Sprintf("casdc.%s.", domain),
			description: "Kerberos authentication for domain controller (UDP)",
		},
		{
			name:     "_kerberos-master._tcp",
			priority: 0,
			weight:   100,
			port:     88,
			target:   fmt.Sprintf("casdc.%s.", domain),
			description: "Kerberos master KDC",
		},
		{
			name:     "_kerberos-master._udp",
			priority: 0,
			weight:   100,
			port:     88,
			target:   fmt.Sprintf("casdc.%s.", domain),
			description: "Kerberos master KDC (UDP)",
		},

		// Kerberos password change service
		{
			name:     "_kpasswd._tcp",
			priority: 0,
			weight:   100,
			port:     464,
			target:   fmt.Sprintf("casdc.%s.", domain),
			description: "Kerberos password change service",
		},
		{
			name:     "_kpasswd._udp",
			priority: 0,
			weight:   100,
			port:     464,
			target:   fmt.Sprintf("casdc.%s.", domain),
			description: "Kerberos password change service (UDP)",
		},

		// Global Catalog service records
		{
			name:     "_gc._tcp",
			priority: 0,
			weight:   100,
			port:     3268,
			target:   fmt.Sprintf("casdc.%s.", domain),
			description: "Global Catalog LDAP service",
		},
		{
			name:     "_gc._tcp.dc._msdcs",
			priority: 0,
			weight:   100,
			port:     3268,
			target:   fmt.Sprintf("casdc.%s.", domain),
			description: "Global Catalog LDAP service for domain controller",
		},

		// Active Directory Web Services (ADWS)
		{
			name:     "_adws._tcp",
			priority: 0,
			weight:   100,
			port:     9389,
			target:   fmt.Sprintf("casdc.%s.", domain),
			description: "Active Directory Web Services",
		},

		// NTP/Time service (for domain time synchronization)
		{
			name:     "_ntp._udp",
			priority: 0,
			weight:   100,
			port:     123,
			target:   fmt.Sprintf("casdc.%s.", domain),
			description: "Network Time Protocol service",
		},
	}

	// Add each SRV record to the zone
	recordsAdded := 0
	for _, srv := range srvRecords {
		record := &Record{
			ZoneID:   zone.ID,
			Name:     srv.name,
			Type:     "SRV",
			Value:    srv.target,
			TTL:      300,
			Priority: srv.priority,
			Weight:   srv.weight,
			Port:     srv.port,
			Enabled:  true,
		}

		// Check if record already exists
		exists := false
		for _, existingRecord := range zone.Records {
			if existingRecord.Name == record.Name && existingRecord.Type == record.Type {
				exists = true
				s.logger.Debug("SRV record %s already exists for %s", record.Name, domain)
				break
			}
		}

		if !exists {
			if err := s.saveRecord(record); err != nil {
				s.logger.Warn("Failed to save SRV record %s: %v", record.Name, err)
				continue
			}

			zone.Records = append(zone.Records, record)
			recordsAdded++
			s.logger.Debug("Created SRV record: %s (%s)", record.Name, srv.description)
		}
	}

	// Add GUID-based DC record (required by Windows)
	// Generate deterministic GUID from domain for consistency
	dcGUID := s.generateDomainGUID(domain)
	guidRecord := &Record{
		ZoneID:  zone.ID,
		Name:    fmt.Sprintf("%s._msdcs", dcGUID),
		Type:    "CNAME",
		Value:   fmt.Sprintf("casdc.%s.", domain),
		TTL:     300,
		Enabled: true,
	}

	if err := s.saveRecord(guidRecord); err != nil {
		s.logger.Warn("Failed to save GUID record: %v", err)
	} else {
		zone.Records = append(zone.Records, guidRecord)
		recordsAdded++
		s.logger.Debug("Created DC GUID record: %s", guidRecord.Name)
	}

	// Regenerate zone file with new records
	if err := s.generateZoneFile(zone); err != nil {
		return fmt.Errorf("failed to regenerate zone file: %w", err)
	}

	// Reload BIND to apply changes
	if err := s.reloadBIND(); err != nil {
		s.logger.Warn("Failed to reload BIND: %v", err)
	}

	s.logger.Info("Created %d Active Directory SRV records for domain %s", recordsAdded, domain)
	return nil
}

// generateDomainGUID generates a deterministic GUID for domain controller identification
func (s *Service) generateDomainGUID(domain string) string {
	// Simple deterministic GUID generation from domain name
	// In production, this would use proper GUID generation with registry
	hash := 0
	for _, c := range domain {
		hash = (hash * 31) + int(c)
	}

	return fmt.Sprintf("%08x-0000-0000-0000-000000000001", hash&0xFFFFFFFF)
}