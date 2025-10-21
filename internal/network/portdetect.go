// Package network provides network configuration utilities including port detection
// and automatic fallback configuration per CASDC SPEC requirements
package network

import (
	"fmt"
	"math/rand"
	"net"
	"time"

	"github.com/casapps/casdc/pkg/logger"
)

// PortConfig represents port configuration with automatic detection
type PortConfig struct {
	HTTPPort      int
	HTTPSPort     int
	DirectMode    bool // true if 80/443 available, false for proxy mode
	ProxyPort     int  // random port if in proxy mode
	ListenAddress string
}

// DetectPorts detects port availability and configures appropriate ports per SPEC
// Direct mode (80/443 available): Bind all interfaces with HTTP→HTTPS redirect and HSTS headers
// Proxy mode (80/443 in use): Random unused port selection, HTTP only initially, localhost binding after setup
func DetectPorts(log *logger.Logger) (*PortConfig, error) {
	config := &PortConfig{
		ListenAddress: "0.0.0.0",
	}

	// Check if port 80 and 443 are available
	port80Available := isPortAvailable("0.0.0.0", 80, log)
	port443Available := isPortAvailable("0.0.0.0", 443, log)

	if port80Available && port443Available {
		// Direct mode: standard HTTP/HTTPS ports available
		config.DirectMode = true
		config.HTTPPort = 80
		config.HTTPSPort = 443
		log.Info("🌐 Direct mode: Ports 80 and 443 available, binding to all interfaces")
	} else {
		// Proxy mode: standard ports in use, need fallback
		config.DirectMode = false
		config.HTTPPort = 0 // Disable HTTP in proxy mode initially
		config.HTTPSPort = 0 // Will be set to random port

		// Find random unused port
		proxyPort, err := findUnusedPort(8000, 9000, log)
		if err != nil {
			return nil, fmt.Errorf("failed to find unused port: %w", err)
		}

		config.ProxyPort = proxyPort
		config.HTTPSPort = proxyPort
		config.ListenAddress = "127.0.0.1" // Localhost binding for proxy mode

		log.Info("🔀 Proxy mode: Ports 80/443 in use, using port %d (localhost only)", proxyPort)
		log.Info("💡 Configure reverse proxy to forward to localhost:%d", proxyPort)
	}

	return config, nil
}

// isPortAvailable checks if a port is available for binding
func isPortAvailable(host string, port int, log *logger.Logger) bool {
	address := fmt.Sprintf("%s:%d", host, port)

	// Try to listen on the port
	listener, err := net.Listen("tcp", address)
	if err != nil {
		log.Debug("Port %d not available: %v", port, err)
		return false
	}

	// Port is available, close the listener
	listener.Close()
	log.Debug("Port %d available", port)
	return true
}

// findUnusedPort finds an unused port in the specified range
func findUnusedPort(start, end int, log *logger.Logger) (int, error) {
	// Seed random number generator
	rand.Seed(time.Now().UnixNano())

	// Try up to 100 random ports in range
	for i := 0; i < 100; i++ {
		port := start + rand.Intn(end-start)

		if isPortAvailable("127.0.0.1", port, log) {
			log.Debug("Found unused port: %d", port)
			return port, nil
		}
	}

	return 0, fmt.Errorf("no unused ports found in range %d-%d", start, end)
}

// GetURLPrefix returns the appropriate URL prefix based on configuration
func (pc *PortConfig) GetURLPrefix(serverAddress string) string {
	if pc.DirectMode {
		// Direct mode: use HTTPS on standard port
		return fmt.Sprintf("https://%s", serverAddress)
	}

	// Proxy mode: show port number
	if pc.ProxyPort == 443 {
		return fmt.Sprintf("https://%s", serverAddress)
	} else if pc.ProxyPort == 80 {
		return fmt.Sprintf("http://%s", serverAddress)
	}

	return fmt.Sprintf("http://%s:%d", serverAddress, pc.ProxyPort)
}

// GetBindAddress returns the address to bind web server to
func (pc *PortConfig) GetBindAddress() string {
	if pc.DirectMode {
		return fmt.Sprintf("%s:%d", pc.ListenAddress, pc.HTTPSPort)
	}

	return fmt.Sprintf("%s:%d", pc.ListenAddress, pc.ProxyPort)
}

// ShouldEnableHTTPS returns whether HTTPS should be enabled
func (pc *PortConfig) ShouldEnableHTTPS() bool {
	return pc.DirectMode
}

// ShouldEnableHTTPRedirect returns whether HTTP→HTTPS redirect should be enabled
func (pc *PortConfig) ShouldEnableHTTPRedirect() bool {
	return pc.DirectMode
}

// GetProxyInstructions returns setup instructions for proxy mode
func (pc *PortConfig) GetProxyInstructions() string {
	if pc.DirectMode {
		return ""
	}

	return fmt.Sprintf(`
┌─────────────────────────────────────────────────────────────────┐
│ PROXY MODE CONFIGURATION REQUIRED                                │
├─────────────────────────────────────────────────────────────────┤
│ Ports 80 and 443 are in use by another service.                 │
│ CASDC is running on localhost:%d                        │
│                                                                   │
│ Configure your reverse proxy (nginx, Apache, etc.) to forward:   │
│                                                                   │
│   External :80  → http://localhost:%d                   │
│   External :443 → https://localhost:%d (with SSL/TLS)   │
│                                                                   │
│ Example nginx configuration:                                     │
│                                                                   │
│   server {                                                        │
│       listen 80;                                                  │
│       listen 443 ssl;                                             │
│       server_name your-domain.com;                                │
│                                                                   │
│       ssl_certificate /path/to/cert.pem;                          │
│       ssl_certificate_key /path/to/key.pem;                       │
│                                                                   │
│       location / {                                                │
│           proxy_pass http://localhost:%d;                │
│           proxy_set_header Host $host;                            │
│           proxy_set_header X-Real-IP $remote_addr;                │
│           proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for; │
│           proxy_set_header X-Forwarded-Proto $scheme;             │
│       }                                                            │
│   }                                                                │
└─────────────────────────────────────────────────────────────────┘
`, pc.ProxyPort, pc.ProxyPort, pc.ProxyPort, pc.ProxyPort)
}

// DetectPrimaryInterface detects the primary network interface
func DetectPrimaryInterface(log *logger.Logger) (*net.Interface, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("failed to get interfaces: %w", err)
	}

	// Find first non-loopback, up interface with an address
	for _, iface := range interfaces {
		// Skip loopback
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		// Must be up
		if iface.Flags&net.FlagUp == 0 {
			continue
		}

		// Skip virtual interfaces (docker, veth, etc.)
		if isVirtualInterface(iface.Name) {
			continue
		}

		// Check if interface has addresses
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok {
				// Check for valid unicast address
				if ipNet.IP.IsGlobalUnicast() || ipNet.IP.IsPrivate() {
					log.Debug("Primary interface detected: %s (%s)", iface.Name, ipNet.IP.String())
					return &iface, nil
				}
			}
		}
	}

	return nil, fmt.Errorf("no suitable network interface found")
}

// isVirtualInterface checks if an interface name indicates a virtual interface
func isVirtualInterface(name string) bool {
	virtualPrefixes := []string{
		"docker",
		"veth",
		"br-",
		"virbr",
		"vboxnet",
		"vmnet",
		"lo",
		"tun",
		"tap",
	}

	for _, prefix := range virtualPrefixes {
		if len(name) >= len(prefix) && name[:len(prefix)] == prefix {
			return true
		}
	}

	return false
}

// GetDefaultRouteIP returns the IP address of the default route interface
func GetDefaultRouteIP(log *logger.Logger) (string, error) {
	iface, err := DetectPrimaryInterface(log)
	if err != nil {
		return "", err
	}

	addrs, err := iface.Addrs()
	if err != nil {
		return "", fmt.Errorf("failed to get interface addresses: %w", err)
	}

	// Return first IPv4 address
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok {
			if ipv4 := ipNet.IP.To4(); ipv4 != nil {
				return ipv4.String(), nil
			}
		}
	}

	// Return first IPv6 address if no IPv4
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok {
			if ipNet.IP.To4() == nil && ipNet.IP.IsGlobalUnicast() {
				return ipNet.IP.String(), nil
			}
		}
	}

	return "", fmt.Errorf("no valid IP address found on interface %s", iface.Name)
}

// CheckPortConflicts checks for port conflicts with common services
func CheckPortConflicts(log *logger.Logger) map[int]string {
	conflicts := make(map[int]string)

	// Common ports to check
	commonPorts := map[int]string{
		80:   "HTTP (likely nginx, Apache, or other web server)",
		443:  "HTTPS (likely nginx, Apache, or other web server)",
		25:   "SMTP (likely postfix, exim, or other mail server)",
		993:  "IMAPS (likely dovecot or other IMAP server)",
		995:  "POP3S (likely dovecot or other POP3 server)",
		53:   "DNS (likely bind9, dnsmasq, or other DNS server)",
		3306: "MySQL/MariaDB database server",
		5432: "PostgreSQL database server",
		6379: "Redis cache server",
	}

	for port, service := range commonPorts {
		if !isPortAvailable("0.0.0.0", port, log) {
			conflicts[port] = service
			log.Debug("Port conflict detected: %d (%s)", port, service)
		}
	}

	return conflicts
}

// GeneratePortReport generates a report of port availability and conflicts
func GeneratePortReport(log *logger.Logger) string {
	conflicts := CheckPortConflicts(log)

	if len(conflicts) == 0 {
		return "✅ All required ports are available"
	}

	report := "⚠️  Port conflicts detected:\n\n"
	for port, service := range conflicts {
		report += fmt.Sprintf("  Port %d: %s\n", port, service)
	}

	report += "\nCASCD will automatically configure proxy mode for conflicting ports.\n"

	return report
}
