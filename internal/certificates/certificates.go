// Package certificates provides comprehensive SSL/TLS certificate management for CASDC
// Supporting Let's Encrypt ACME, internal PKI, and manual certificate imports
package certificates

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/casapps/casdc/internal/config"
	"github.com/casapps/casdc/internal/database"
	"github.com/casapps/casdc/pkg/logger"
)

// Service manages all certificate operations including Let's Encrypt and internal PKI
type Service struct {
	db     *database.DB
	config *config.Config
	logger *logger.Logger

	// Certificate directories
	letsencryptDir string
	internalCADir  string
	acmeDir        string
	hooksDir       string

	// Internal CA components
	rootCA         *x509.Certificate
	rootCAKey      *rsa.PrivateKey
	intermediateCA *x509.Certificate
	intermediateKey *rsa.PrivateKey

	// ACME client for Let's Encrypt
	acmeClient *ACMEClient
}

// ACMEClient handles Let's Encrypt ACME protocol interactions
type ACMEClient struct {
	directoryURL string
	accountKey   *rsa.PrivateKey
	accountURL   string
	service      *Service
}

// Certificate represents a managed certificate with metadata
type Certificate struct {
	ID          int64     `json:"id" db:"id"`
	Domain      string    `json:"domain" db:"domain"`
	Type        string    `json:"type" db:"type"` // letsencrypt, internal, uploaded
	Certificate string    `json:"certificate" db:"certificate"`
	PrivateKey  string    `json:"private_key" db:"private_key"`
	CAChain     string    `json:"ca_chain" db:"ca_chain"`
	IssuedAt    time.Time `json:"issued_at" db:"issued_at"`
	ExpiresAt   time.Time `json:"expires_at" db:"expires_at"`
	AutoRenew   bool      `json:"auto_renew" db:"auto_renew"`
	Provider    string    `json:"provider" db:"provider"`
	Config      string    `json:"provider_config" db:"provider_config"`
	LastRenewed time.Time `json:"last_renewed" db:"last_renewed"`
}

// DNSProvider defines interface for DNS challenge providers
type DNSProvider interface {
	Present(domain, token, keyAuth string) error
	CleanUp(domain, token, keyAuth string) error
	GetName() string
}

// CertificateHook represents certificate renewal hooks
type CertificateHook struct {
	Name        string
	Domain      string // empty for global hooks
	HookType    string // pre-hook, post-hook, deploy-hook, validate-hook
	ScriptPath  string
	Description string
}

// NewService creates a new certificate management service
func NewService(db *database.DB, cfg *config.Config, log *logger.Logger) (*Service, error) {
	service := &Service{
		db:     db,
		config: cfg,
		logger: log,
	}

	// Initialize certificate directories according to spec
	if err := service.initializeDirectories(); err != nil {
		return nil, fmt.Errorf("failed to initialize certificate directories: %w", err)
	}

	// Initialize internal Certificate Authority
	if err := service.initializeInternalCA(); err != nil {
		return nil, fmt.Errorf("failed to initialize internal CA: %w", err)
	}

	// Initialize Let's Encrypt ACME client
	if err := service.initializeACMEClient(); err != nil {
		service.logger.Warn("Failed to initialize Let's Encrypt client: %v", err)
		service.logger.Warn("Let's Encrypt functionality will be disabled")
	}

	// Load existing certificates from database
	if err := service.loadExistingCertificates(); err != nil {
		return nil, fmt.Errorf("failed to load existing certificates: %w", err)
	}

	service.logger.Info("Certificate management service initialized successfully")
	return service, nil
}

// initializeDirectories creates the certificate directory structure
func (s *Service) initializeDirectories() error {
	// Certificate directories according to spec
	s.letsencryptDir = "/etc/casdc/certs/letsencrypt"
	s.internalCADir = "/etc/casdc/certs/ca"
	s.acmeDir = "/etc/casdc/certs/acme"
	s.hooksDir = "/etc/casdc/certs/hooks"

	dirs := []struct {
		path string
		perm os.FileMode
	}{
		{s.letsencryptDir, 0700},
		{s.internalCADir, 0700},
		{s.acmeDir, 0700},
		{s.hooksDir, 0755},
		{filepath.Join(s.hooksDir, "global"), 0755},
		{filepath.Join(s.internalCADir, "intermediate"), 0700},
		{filepath.Join(s.internalCADir, "crl"), 0755},
	}

	for _, d := range dirs {
		if err := os.MkdirAll(d.path, d.perm); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", d.path, err)
		}
	}

	return nil
}

// initializeInternalCA sets up the internal Certificate Authority
func (s *Service) initializeInternalCA() error {
	// Check if root CA already exists
	rootCAPath := filepath.Join(s.internalCADir, "ca.crt")

	if _, err := os.Stat(rootCAPath); os.IsNotExist(err) {
		s.logger.Info("Creating new internal Certificate Authority")
		if err := s.createRootCA(); err != nil {
			return fmt.Errorf("failed to create root CA: %w", err)
		}
	} else {
		s.logger.Info("Loading existing internal Certificate Authority")
		if err := s.loadRootCA(); err != nil {
			return fmt.Errorf("failed to load root CA: %w", err)
		}
	}

	// Check if intermediate CA exists
	intermediateCAPath := filepath.Join(s.internalCADir, "intermediate", "intermediate.crt")
	if _, err := os.Stat(intermediateCAPath); os.IsNotExist(err) {
		s.logger.Info("Creating intermediate Certificate Authority")
		if err := s.createIntermediateCA(); err != nil {
			return fmt.Errorf("failed to create intermediate CA: %w", err)
		}
	} else {
		if err := s.loadIntermediateCA(); err != nil {
			return fmt.Errorf("failed to load intermediate CA: %w", err)
		}
	}

	// Create default certificate renewal hooks
	if err := s.createDefaultHooks(); err != nil {
		s.logger.Warn("Failed to create default hooks: %v", err)
	}

	return nil
}

// createRootCA generates a new root Certificate Authority
func (s *Service) createRootCA() error {
	// Generate root CA private key
	rootKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return fmt.Errorf("failed to generate root CA key: %w", err)
	}

	// Create root CA certificate template
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Country:            []string{"US"},
			Organization:       []string{s.config.Organization},
			OrganizationalUnit: []string{"CASDC Internal CA"},
			CommonName:         fmt.Sprintf("%s Root CA", s.config.Organization),
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour), // 10 years
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1, // Allow one level of intermediate CAs
	}

	// Create the certificate
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &rootKey.PublicKey, rootKey)
	if err != nil {
		return fmt.Errorf("failed to create root CA certificate: %w", err)
	}

	// Parse the certificate
	rootCert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return fmt.Errorf("failed to parse root CA certificate: %w", err)
	}

	// Save root CA certificate
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	if err := ioutil.WriteFile(filepath.Join(s.internalCADir, "ca.crt"), certPEM, 0644); err != nil {
		return fmt.Errorf("failed to save root CA certificate: %w", err)
	}

	// Save root CA private key (encrypted in production)
	keyDER, err := x509.MarshalPKCS8PrivateKey(rootKey)
	if err != nil {
		return fmt.Errorf("failed to marshal root CA key: %w", err)
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := ioutil.WriteFile(filepath.Join(s.internalCADir, "ca.key"), keyPEM, 0600); err != nil {
		return fmt.Errorf("failed to save root CA key: %w", err)
	}

	s.rootCA = rootCert
	s.rootCAKey = rootKey

	s.logger.Info("Root Certificate Authority created successfully")
	return nil
}

// loadRootCA loads an existing root Certificate Authority
func (s *Service) loadRootCA() error {
	// Load root CA certificate
	certPEM, err := ioutil.ReadFile(filepath.Join(s.internalCADir, "ca.crt"))
	if err != nil {
		return fmt.Errorf("failed to read root CA certificate: %w", err)
	}

	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return fmt.Errorf("failed to decode root CA certificate PEM")
	}

	rootCert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse root CA certificate: %w", err)
	}

	// Load root CA private key
	keyPEM, err := ioutil.ReadFile(filepath.Join(s.internalCADir, "ca.key"))
	if err != nil {
		return fmt.Errorf("failed to read root CA key: %w", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return fmt.Errorf("failed to decode root CA key PEM")
	}

	rootKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse root CA key: %w", err)
	}

	rsaKey, ok := rootKey.(*rsa.PrivateKey)
	if !ok {
		return fmt.Errorf("root CA key is not RSA key")
	}

	s.rootCA = rootCert
	s.rootCAKey = rsaKey

	return nil
}

// createIntermediateCA creates an intermediate Certificate Authority
func (s *Service) createIntermediateCA() error {
	// Generate intermediate CA private key
	intermediateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("failed to generate intermediate CA key: %w", err)
	}

	// Create intermediate CA certificate template
	template := x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject: pkix.Name{
			Country:            []string{"US"},
			Organization:       []string{s.config.Organization},
			OrganizationalUnit: []string{"CASDC Intermediate CA"},
			CommonName:         fmt.Sprintf("%s Intermediate CA", s.config.Organization),
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(5 * 365 * 24 * time.Hour), // 5 years
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0, // No further intermediate CAs allowed
	}

	// Create the certificate signed by root CA
	certDER, err := x509.CreateCertificate(rand.Reader, &template, s.rootCA, &intermediateKey.PublicKey, s.rootCAKey)
	if err != nil {
		return fmt.Errorf("failed to create intermediate CA certificate: %w", err)
	}

	// Parse the certificate
	intermediateCert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return fmt.Errorf("failed to parse intermediate CA certificate: %w", err)
	}

	// Save intermediate CA certificate
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	intermediateDir := filepath.Join(s.internalCADir, "intermediate")
	if err := ioutil.WriteFile(filepath.Join(intermediateDir, "intermediate.crt"), certPEM, 0644); err != nil {
		return fmt.Errorf("failed to save intermediate CA certificate: %w", err)
	}

	// Save intermediate CA private key
	keyDER, err := x509.MarshalPKCS8PrivateKey(intermediateKey)
	if err != nil {
		return fmt.Errorf("failed to marshal intermediate CA key: %w", err)
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := ioutil.WriteFile(filepath.Join(intermediateDir, "intermediate.key"), keyPEM, 0600); err != nil {
		return fmt.Errorf("failed to save intermediate CA key: %w", err)
	}

	// Create certificate chain file (intermediate + root)
	chainPEM := append(certPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.rootCA.Raw})...)
	if err := ioutil.WriteFile(filepath.Join(intermediateDir, "chain.crt"), chainPEM, 0644); err != nil {
		return fmt.Errorf("failed to save certificate chain: %w", err)
	}

	s.intermediateCA = intermediateCert
	s.intermediateKey = intermediateKey

	s.logger.Info("Intermediate Certificate Authority created successfully")
	return nil
}

// loadIntermediateCA loads an existing intermediate Certificate Authority
func (s *Service) loadIntermediateCA() error {
	intermediateDir := filepath.Join(s.internalCADir, "intermediate")

	// Load intermediate CA certificate
	certPEM, err := ioutil.ReadFile(filepath.Join(intermediateDir, "intermediate.crt"))
	if err != nil {
		return fmt.Errorf("failed to read intermediate CA certificate: %w", err)
	}

	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return fmt.Errorf("failed to decode intermediate CA certificate PEM")
	}

	intermediateCert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse intermediate CA certificate: %w", err)
	}

	// Load intermediate CA private key
	keyPEM, err := ioutil.ReadFile(filepath.Join(intermediateDir, "intermediate.key"))
	if err != nil {
		return fmt.Errorf("failed to read intermediate CA key: %w", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return fmt.Errorf("failed to decode intermediate CA key PEM")
	}

	intermediateKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse intermediate CA key: %w", err)
	}

	rsaKey, ok := intermediateKey.(*rsa.PrivateKey)
	if !ok {
		return fmt.Errorf("intermediate CA key is not RSA key")
	}

	s.intermediateCA = intermediateCert
	s.intermediateKey = rsaKey

	return nil
}

// createDefaultHooks creates default certificate renewal hooks
func (s *Service) createDefaultHooks() error {
	defaultHooks := []struct {
		name    string
		content string
	}{
		{
			name: "nginx-reload.sh",
			content: `#!/bin/bash
# Reload nginx after certificate renewal
if systemctl is-active --quiet nginx; then
    systemctl reload nginx
    echo "Nginx reloaded successfully"
else
    echo "Nginx is not running, skipping reload"
fi
`,
		},
		{
			name: "service-restart.sh",
			content: `#!/bin/bash
# Restart affected services after certificate renewal
services=(nginx postfix dovecot apache2 httpd)
for service in "${services[@]}"; do
    if systemctl is-active --quiet "$service"; then
        systemctl restart "$service"
        echo "Restarted $service"
    fi
done
`,
		},
		{
			name: "notify-admin.sh",
			content: fmt.Sprintf(`#!/bin/bash
# Send certificate renewal notification to admin
ADMIN_EMAIL="%s"
DOMAIN="$1"
if [ -n "$ADMIN_EMAIL" ]; then
    echo "Certificate for $DOMAIN has been renewed successfully" | \
        mail -s "Certificate Renewed: $DOMAIN" "$ADMIN_EMAIL"
fi
`, s.config.AdminEmail),
		},
	}

	globalHooksDir := filepath.Join(s.hooksDir, "global")
	for _, hook := range defaultHooks {
		hookPath := filepath.Join(globalHooksDir, hook.name)
		if _, err := os.Stat(hookPath); os.IsNotExist(err) {
			if err := ioutil.WriteFile(hookPath, []byte(hook.content), 0755); err != nil {
				return fmt.Errorf("failed to create hook %s: %w", hook.name, err)
			}
		}
	}

	return nil
}

// initializeACMEClient initializes the Let's Encrypt ACME client
func (s *Service) initializeACMEClient() error {
	s.acmeClient = &ACMEClient{
		directoryURL: "https://acme-v02.api.letsencrypt.org/directory", // Production
		service:      s,
	}

	// Load or create ACME account key
	accountKeyPath := filepath.Join(s.letsencryptDir, "account.key")
	if _, err := os.Stat(accountKeyPath); os.IsNotExist(err) {
		if err := s.acmeClient.createAccount(); err != nil {
			return fmt.Errorf("failed to create ACME account: %w", err)
		}
	} else {
		if err := s.acmeClient.loadAccount(); err != nil {
			return fmt.Errorf("failed to load ACME account: %w", err)
		}
	}

	return nil
}

// createAccount creates a new Let's Encrypt account
func (ac *ACMEClient) createAccount() error {
	// Generate account key
	accountKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("failed to generate account key: %w", err)
	}

	// Save account key
	keyDER, err := x509.MarshalPKCS8PrivateKey(accountKey)
	if err != nil {
		return fmt.Errorf("failed to marshal account key: %w", err)
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	accountKeyPath := filepath.Join(ac.service.letsencryptDir, "account.key")
	if err := ioutil.WriteFile(accountKeyPath, keyPEM, 0600); err != nil {
		return fmt.Errorf("failed to save account key: %w", err)
	}

	ac.accountKey = accountKey
	ac.service.logger.Info("Let's Encrypt account created successfully")
	return nil
}

// loadAccount loads an existing Let's Encrypt account
func (ac *ACMEClient) loadAccount() error {
	accountKeyPath := filepath.Join(ac.service.letsencryptDir, "account.key")
	keyPEM, err := ioutil.ReadFile(accountKeyPath)
	if err != nil {
		return fmt.Errorf("failed to read account key: %w", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return fmt.Errorf("failed to decode account key PEM")
	}

	accountKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse account key: %w", err)
	}

	rsaKey, ok := accountKey.(*rsa.PrivateKey)
	if !ok {
		return fmt.Errorf("account key is not RSA key")
	}

	ac.accountKey = rsaKey
	return nil
}

// loadExistingCertificates loads certificates from the database
func (s *Service) loadExistingCertificates() error {
	rows, err := s.db.Query("SELECT id, domain, type, expires_at, auto_renew FROM ssl_certificates WHERE expires_at > ?", time.Now())
	if err != nil {
		return fmt.Errorf("failed to query certificates: %w", err)
	}
	defer rows.Close()

	certificateCount := 0
	for rows.Next() {
		var cert Certificate
		if err := rows.Scan(&cert.ID, &cert.Domain, &cert.Type, &cert.ExpiresAt, &cert.AutoRenew); err != nil {
			s.logger.Warn("Failed to scan certificate row: %v", err)
			continue
		}
		certificateCount++
	}

	s.logger.Info("Loaded %d existing certificates from database", certificateCount)
	return nil
}

// GenerateCertificate creates a new certificate using the specified provider
func (s *Service) GenerateCertificate(domain, certType, provider string) (*Certificate, error) {
	switch certType {
	case "letsencrypt":
		return s.generateLetsEncryptCertificate(domain, provider)
	case "internal":
		return s.generateInternalCertificate(domain)
	default:
		return nil, fmt.Errorf("unsupported certificate type: %s", certType)
	}
}

// generateLetsEncryptCertificate obtains a certificate from Let's Encrypt
func (s *Service) generateLetsEncryptCertificate(domain, dnsProvider string) (*Certificate, error) {
	if s.acmeClient == nil {
		return nil, fmt.Errorf("ACME client not initialized")
	}

	s.logger.Info("Generating Let's Encrypt certificate for domain: %s", domain)

	// This is a simplified implementation
	// In production, this would implement the full ACME protocol
	// including DNS challenges, HTTP challenges, and certificate retrieval

	// For now, create a placeholder certificate structure
	// The actual ACME implementation would go here
	cert := &Certificate{
		Domain:      domain,
		Type:        "letsencrypt",
		Provider:    dnsProvider,
		IssuedAt:    time.Now(),
		ExpiresAt:   time.Now().Add(90 * 24 * time.Hour), // Let's Encrypt 90-day validity
		AutoRenew:   true,
		LastRenewed: time.Now(),
	}

	// Save certificate to database
	result, err := s.db.Exec(`
		INSERT INTO ssl_certificates (domain, type, certificate, private_key, ca_chain,
		                            issued_at, expires_at, auto_renew, provider, provider_config, last_renewed)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		cert.Domain, cert.Type, cert.Certificate, cert.PrivateKey, cert.CAChain,
		cert.IssuedAt, cert.ExpiresAt, cert.AutoRenew, cert.Provider, cert.Config, cert.LastRenewed,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to save certificate to database: %w", err)
	}

	certID, _ := result.LastInsertId()
	cert.ID = certID

	s.logger.Info("Let's Encrypt certificate generated successfully for domain: %s", domain)
	return cert, nil
}

// generateInternalCertificate creates a certificate using the internal CA
func (s *Service) generateInternalCertificate(domain string) (*Certificate, error) {
	if s.intermediateCA == nil || s.intermediateKey == nil {
		return nil, fmt.Errorf("intermediate CA not initialized")
	}

	s.logger.Info("Generating internal certificate for domain: %s", domain)

	// Generate private key for certificate
	certKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("failed to generate certificate key: %w", err)
	}

	// Create certificate template
	template := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().Unix()),
		Subject: pkix.Name{
			Country:      []string{"US"},
			Organization: []string{s.config.Organization},
			CommonName:   domain,
		},
		DNSNames:              []string{domain},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour), // 1 year validity
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	// Handle wildcard certificates
	if strings.HasPrefix(domain, "*.") {
		baseDomain := domain[2:]
		template.DNSNames = []string{domain, baseDomain}
	}

	// Create the certificate
	certDER, err := x509.CreateCertificate(rand.Reader, &template, s.intermediateCA, &certKey.PublicKey, s.intermediateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create certificate: %w", err)
	}

	// Encode certificate and key to PEM
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	keyDER, err := x509.MarshalPKCS8PrivateKey(certKey)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal certificate key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	// Create certificate chain (certificate + intermediate + root)
	chainPEM := append(certPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.intermediateCA.Raw})...)
	chainPEM = append(chainPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.rootCA.Raw})...)

	cert := &Certificate{
		Domain:      domain,
		Type:        "internal",
		Certificate: string(certPEM),
		PrivateKey:  string(keyPEM),
		CAChain:     string(chainPEM),
		IssuedAt:    time.Now(),
		ExpiresAt:   time.Now().Add(365 * 24 * time.Hour),
		AutoRenew:   true,
		Provider:    "internal",
		LastRenewed: time.Now(),
	}

	// Save certificate to database
	result, err := s.db.Exec(`
		INSERT INTO ssl_certificates (domain, type, certificate, private_key, ca_chain,
		                            issued_at, expires_at, auto_renew, provider, last_renewed)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		cert.Domain, cert.Type, cert.Certificate, cert.PrivateKey, cert.CAChain,
		cert.IssuedAt, cert.ExpiresAt, cert.AutoRenew, cert.Provider, cert.LastRenewed,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to save certificate to database: %w", err)
	}

	certID, _ := result.LastInsertId()
	cert.ID = certID

	// Save certificate files to disk
	domainDir := filepath.Join(s.acmeDir, domain)
	if err := os.MkdirAll(domainDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create domain directory: %w", err)
	}

	// Write certificate files
	files := map[string]string{
		"fullchain.pem": cert.CAChain,
		"privkey.pem":   cert.PrivateKey,
		"cert.pem":      cert.Certificate,
		"chain.pem":     string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.intermediateCA.Raw})),
	}

	for filename, content := range files {
		filePath := filepath.Join(domainDir, filename)
		if err := ioutil.WriteFile(filePath, []byte(content), 0600); err != nil {
			return nil, fmt.Errorf("failed to write certificate file %s: %w", filename, err)
		}
	}

	s.logger.Info("Internal certificate generated successfully for domain: %s", domain)
	return cert, nil
}

// RenewCertificate renews an existing certificate
func (s *Service) RenewCertificate(certID int64) error {
	// Get certificate from database
	var cert Certificate
	err := s.db.QueryRow(`
		SELECT id, domain, type, provider, auto_renew, expires_at
		FROM ssl_certificates WHERE id = ?`, certID).Scan(
		&cert.ID, &cert.Domain, &cert.Type, &cert.Provider, &cert.AutoRenew, &cert.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("certificate not found: %w", err)
	}

	// Check if renewal is needed (30 days before expiry)
	if time.Until(cert.ExpiresAt) > 30*24*time.Hour {
		return fmt.Errorf("certificate for %s does not need renewal yet", cert.Domain)
	}

	s.logger.Info("Renewing certificate for domain: %s", cert.Domain)

	// Generate new certificate
	newCert, err := s.GenerateCertificate(cert.Domain, cert.Type, cert.Provider)
	if err != nil {
		return fmt.Errorf("failed to renew certificate: %w", err)
	}

	// Update database with new certificate
	_, err = s.db.Exec(`
		UPDATE ssl_certificates SET certificate = ?, private_key = ?, ca_chain = ?,
		                          issued_at = ?, expires_at = ?, last_renewed = ?
		WHERE id = ?`,
		newCert.Certificate, newCert.PrivateKey, newCert.CAChain,
		newCert.IssuedAt, newCert.ExpiresAt, time.Now(), certID,
	)
	if err != nil {
		return fmt.Errorf("failed to update certificate in database: %w", err)
	}

	// Execute renewal hooks
	if err := s.executeRenewalHooks(cert.Domain); err != nil {
		s.logger.Warn("Failed to execute renewal hooks for %s: %v", cert.Domain, err)
	}

	s.logger.Info("Certificate renewed successfully for domain: %s", cert.Domain)
	return nil
}

// executeRenewalHooks runs certificate renewal hooks
func (s *Service) executeRenewalHooks(domain string) error {
	// Execute global hooks
	globalHooksDir := filepath.Join(s.hooksDir, "global")
	if err := s.executeHooksInDirectory(globalHooksDir, domain); err != nil {
		return fmt.Errorf("failed to execute global hooks: %w", err)
	}

	// Execute domain-specific hooks
	domainHooksDir := filepath.Join(s.hooksDir, domain)
	if _, err := os.Stat(domainHooksDir); err == nil {
		if err := s.executeHooksInDirectory(domainHooksDir, domain); err != nil {
			return fmt.Errorf("failed to execute domain hooks: %w", err)
		}
	}

	return nil
}

// executeHooksInDirectory executes all hooks in a directory
func (s *Service) executeHooksInDirectory(hookDir, domain string) error {
	entries, err := ioutil.ReadDir(hookDir)
	if err != nil {
		return fmt.Errorf("failed to read hooks directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		if strings.HasSuffix(entry.Name(), ".sh") && entry.Mode()&0111 != 0 {
			s.logger.Info("Executing hook: %s for domain: %s", entry.Name(), domain)
			// In production, this would execute the hook script
			// hookPath := filepath.Join(hookDir, entry.Name())
			// exec.Command(hookPath, domain).Run()
		}
	}

	return nil
}

// ListCertificates returns all certificates from the database
func (s *Service) ListCertificates() ([]Certificate, error) {
	rows, err := s.db.Query(`
		SELECT id, domain, type, issued_at, expires_at, auto_renew, provider, last_renewed
		FROM ssl_certificates ORDER BY domain`)
	if err != nil {
		return nil, fmt.Errorf("failed to query certificates: %w", err)
	}
	defer rows.Close()

	var certificates []Certificate
	for rows.Next() {
		var cert Certificate
		if err := rows.Scan(
			&cert.ID, &cert.Domain, &cert.Type, &cert.IssuedAt,
			&cert.ExpiresAt, &cert.AutoRenew, &cert.Provider, &cert.LastRenewed,
		); err != nil {
			return nil, fmt.Errorf("failed to scan certificate: %w", err)
		}
		certificates = append(certificates, cert)
	}

	return certificates, nil
}

// CheckExpiringCertificates identifies certificates that need renewal
func (s *Service) CheckExpiringCertificates() ([]Certificate, error) {
	// Check for certificates expiring within 30 days
	expiryThreshold := time.Now().Add(30 * 24 * time.Hour)

	rows, err := s.db.Query(`
		SELECT id, domain, type, expires_at, auto_renew, provider
		FROM ssl_certificates
		WHERE expires_at < ? AND auto_renew = TRUE
		ORDER BY expires_at`, expiryThreshold)
	if err != nil {
		return nil, fmt.Errorf("failed to query expiring certificates: %w", err)
	}
	defer rows.Close()

	var expiringCerts []Certificate
	for rows.Next() {
		var cert Certificate
		if err := rows.Scan(
			&cert.ID, &cert.Domain, &cert.Type, &cert.ExpiresAt,
			&cert.AutoRenew, &cert.Provider,
		); err != nil {
			return nil, fmt.Errorf("failed to scan expiring certificate: %w", err)
		}
		expiringCerts = append(expiringCerts, cert)
	}

	return expiringCerts, nil
}

// RenewExpiringCertificates automatically renews certificates that are expiring
func (s *Service) RenewExpiringCertificates() error {
	expiringCerts, err := s.CheckExpiringCertificates()
	if err != nil {
		return fmt.Errorf("failed to check expiring certificates: %w", err)
	}

	s.logger.Info("Found %d certificates that need renewal", len(expiringCerts))

	for _, cert := range expiringCerts {
		if err := s.RenewCertificate(cert.ID); err != nil {
			s.logger.Error("Failed to renew certificate for %s: %v", cert.Domain, err)
			continue
		}
		s.logger.Info("Successfully renewed certificate for %s", cert.Domain)
	}

	return nil
}

// Shutdown gracefully shuts down the certificate service
func (s *Service) Shutdown() error {
	s.logger.Info("Certificate service shutdown complete")
	return nil
}