// Package pxe implements PXE boot server for network installation
// Provides network boot capabilities for Linux distribution installation
// and hardware provisioning as per CASDC specification
package pxe

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/casapps/casdc/internal/config"
	"github.com/casapps/casdc/internal/database"
	"github.com/casapps/casdc/pkg/logger"
)

// Service handles PXE boot server operations
type Service struct {
	db     *database.DB
	config *config.Config
	logger *logger.Logger

	// PXE server configuration
	tftpRoot     string
	httpRoot     string
	pxelinuxDir  string
	imagesDir    string
	preseedDir   string
	kickstartDir string

	// Boot menu management
	bootMenu     *BootMenu
	bootEntries  map[string]*BootEntry
	entriesMutex sync.RWMutex

	// TFTP server
	tftpServer *TFTPServer
	httpServer *HTTPServer
}

// BootMenu represents the PXE boot menu configuration
type BootMenu struct {
	Title          string
	Timeout        int // seconds
	DefaultEntry   string
	Entries        []*BootEntry
	OrganizationName string
}

// BootEntry represents a single boot menu entry
type BootEntry struct {
	ID          string
	Label       string
	Description string
	Type        string // linux, rescue, diagnostic, wipe, custom, local
	KernelPath  string
	InitrdPath  string
	KernelArgs  string
	AutoInstall bool
	PreseedURL  string
	KickstartURL string
	Enabled     bool
}

// TFTPServer handles TFTP protocol for PXE boot files
type TFTPServer struct {
	address  string
	port     int
	conn     *net.UDPConn
	rootDir  string
	logger   *logger.Logger
	stopChan chan struct{}
}

// HTTPServer handles HTTP downloads for installation files
type HTTPServer struct {
	address string
	port    int
	rootDir string
	logger  *logger.Logger
}

// NewService creates a new PXE boot service instance
func NewService(db *database.DB, cfg *config.Config, log *logger.Logger) (*Service, error) {
	s := &Service{
		db:     db,
		config: cfg,
		logger: log,

		// PXE directory structure
		tftpRoot:     "/var/lib/casdc/pxe/tftp",
		httpRoot:     "/var/lib/casdc/pxe/http",
		pxelinuxDir:  "/var/lib/casdc/pxe/tftp/pxelinux",
		imagesDir:    "/var/lib/casdc/pxe/images",
		preseedDir:   "/var/lib/casdc/pxe/preseed",
		kickstartDir: "/var/lib/casdc/pxe/kickstart",

		bootEntries: make(map[string]*BootEntry),
	}

	// Create directory structure
	if err := s.ensureDirectories(); err != nil {
		return nil, fmt.Errorf("failed to create PXE directories: %w", err)
	}

	// Initialize boot menu
	s.bootMenu = s.createDefaultBootMenu()

	// Generate PXE configuration files
	if err := s.generatePXEConfig(); err != nil {
		return nil, fmt.Errorf("failed to generate PXE configuration: %w", err)
	}

	// Initialize TFTP server
	tftpServer, err := NewTFTPServer(s.tftpRoot, 69, log)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize TFTP server: %w", err)
	}
	s.tftpServer = tftpServer

	// Initialize HTTP server for large file downloads
	httpServer := NewHTTPServer(s.httpRoot, 8080, log)
	s.httpServer = httpServer

	return s, nil
}

// ensureDirectories creates the PXE directory structure
func (s *Service) ensureDirectories() error {
	dirs := []struct {
		path string
		perm os.FileMode
	}{
		{s.tftpRoot, 0755},
		{s.httpRoot, 0755},
		{s.pxelinuxDir, 0755},
		{filepath.Join(s.pxelinuxDir, "cfg"), 0755},
		{s.imagesDir, 0755},
		{filepath.Join(s.imagesDir, "ubuntu"), 0755},
		{filepath.Join(s.imagesDir, "centos"), 0755},
		{filepath.Join(s.imagesDir, "debian"), 0755},
		{s.preseedDir, 0755},
		{s.kickstartDir, 0755},
	}

	for _, d := range dirs {
		if err := os.MkdirAll(d.path, d.perm); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", d.path, err)
		}
	}

	return nil
}

// createDefaultBootMenu creates the default PXE boot menu
func (s *Service) createDefaultBootMenu() *BootMenu {
	return &BootMenu{
		Title:            "CASDC Network Boot Menu",
		Timeout:          30,
		DefaultEntry:     "ubuntu-2204",
		OrganizationName: s.config.Organization,
		Entries: []*BootEntry{
			{
				ID:          "ubuntu-2204",
				Label:       "Install Ubuntu 22.04 LTS (Automated)",
				Description: "Automated Ubuntu 22.04 LTS installation with CASDC domain join",
				Type:        "linux",
				KernelPath:  "images/ubuntu/vmlinuz",
				InitrdPath:  "images/ubuntu/initrd",
				KernelArgs:  "auto=true priority=critical preseed/url=http://{serverip}:8080/preseed/ubuntu.cfg",
				AutoInstall: true,
				PreseedURL:  "http://{serverip}:8080/preseed/ubuntu.cfg",
				Enabled:     true,
			},
			{
				ID:           "centos-9",
				Label:        "Install CentOS Stream 9 (Automated)",
				Description:  "Automated CentOS Stream 9 installation with CASDC domain join",
				Type:         "linux",
				KernelPath:   "images/centos/vmlinuz",
				InitrdPath:   "images/centos/initrd.img",
				KernelArgs:   "inst.ks=http://{serverip}:8080/kickstart/centos.cfg",
				AutoInstall:  true,
				KickstartURL: "http://{serverip}:8080/kickstart/centos.cfg",
				Enabled:      true,
			},
			{
				ID:          "debian-12",
				Label:       "Install Debian 12 (Automated)",
				Description: "Automated Debian 12 installation with CASDC domain join",
				Type:        "linux",
				KernelPath:  "images/debian/vmlinuz",
				InitrdPath:  "images/debian/initrd.gz",
				KernelArgs:  "auto=true priority=critical preseed/url=http://{serverip}:8080/preseed/debian.cfg",
				AutoInstall: true,
				PreseedURL:  "http://{serverip}:8080/preseed/debian.cfg",
				Enabled:     true,
			},
			{
				ID:          "rescue",
				Label:       "Rescue/Recovery Environment",
				Description: "Boot into rescue environment for system recovery",
				Type:        "rescue",
				KernelPath:  "images/rescue/vmlinuz",
				InitrdPath:  "images/rescue/initrd",
				KernelArgs:  "rescue",
				Enabled:     true,
			},
			{
				ID:          "diagnostics",
				Label:       "Hardware Diagnostics",
				Description: "Run hardware diagnostics and stress tests",
				Type:        "diagnostic",
				KernelPath:  "images/diag/vmlinuz",
				InitrdPath:  "images/diag/initrd",
				KernelArgs:  "diagnostic memtest cpuburn",
				Enabled:     true,
			},
			{
				ID:          "wipe",
				Label:       "Disk Wiping and Secure Erase",
				Description: "Securely wipe all disks (WARNING: DESTRUCTIVE)",
				Type:        "wipe",
				KernelPath:  "images/wipe/vmlinuz",
				InitrdPath:  "images/wipe/initrd",
				KernelArgs:  "nuke",
				Enabled:     true,
			},
			{
				ID:          "local",
				Label:       "Boot from Local Disk",
				Description: "Exit PXE and boot from local hard drive",
				Type:        "local",
				Enabled:     true,
			},
		},
	}
}

// generatePXEConfig generates the PXE boot configuration files
func (s *Service) generatePXEConfig() error {
	// Generate pxelinux.cfg/default file
	configPath := filepath.Join(s.pxelinuxDir, "cfg", "default")

	var config strings.Builder

	// SPEC: Boot menu configuration with organization branding
	config.WriteString("# CASDC Network Boot Menu\n")
	config.WriteString(fmt.Sprintf("# %s\n", s.config.Organization))
	config.WriteString("# Generated automatically - DO NOT EDIT\n\n")

	config.WriteString("DEFAULT menu.c32\n")
	config.WriteString(fmt.Sprintf("TIMEOUT %d\n", s.bootMenu.Timeout*10)) // timeout in 1/10th seconds
	config.WriteString("PROMPT 0\n\n")

	// Menu title and appearance
	config.WriteString(fmt.Sprintf("MENU TITLE %s\n", s.bootMenu.Title))
	config.WriteString("MENU BACKGROUND splash.png\n")
	config.WriteString("MENU COLOR screen 37;40 #80ffffff #00000000 std\n")
	config.WriteString("MENU COLOR border 30;44 #40ffffff #00000000 std\n")
	config.WriteString("MENU COLOR title 1;36;44 #ffffffff #00000000 std\n")
	config.WriteString("MENU COLOR sel 7;37;40 #e0000000 #20ff8000 all\n\n")

	// Generate menu entries
	for _, entry := range s.bootMenu.Entries {
		if !entry.Enabled {
			continue
		}

		config.WriteString(fmt.Sprintf("LABEL %s\n", entry.ID))
		config.WriteString(fmt.Sprintf("  MENU LABEL %s\n", entry.Label))
		if entry.Description != "" {
			config.WriteString(fmt.Sprintf("  TEXT HELP\n    %s\n  ENDTEXT\n", entry.Description))
		}

		switch entry.Type {
		case "linux":
			config.WriteString(fmt.Sprintf("  KERNEL %s\n", entry.KernelPath))
			config.WriteString(fmt.Sprintf("  APPEND initrd=%s %s\n", entry.InitrdPath, s.replaceVariables(entry.KernelArgs)))
		case "local":
			config.WriteString("  LOCALBOOT 0\n")
		default:
			config.WriteString(fmt.Sprintf("  KERNEL %s\n", entry.KernelPath))
			config.WriteString(fmt.Sprintf("  APPEND initrd=%s %s\n", entry.InitrdPath, entry.KernelArgs))
		}

		config.WriteString("\n")
	}

	// Write configuration file
	if err := os.WriteFile(configPath, []byte(config.String()), 0644); err != nil {
		return fmt.Errorf("failed to write PXE config: %w", err)
	}

	s.logger.Info("Generated PXE boot configuration with %d entries", len(s.bootMenu.Entries))
	return nil
}

// replaceVariables replaces template variables in configuration strings
func (s *Service) replaceVariables(input string) string {
	output := input
	output = strings.ReplaceAll(output, "{serverip}", s.config.ServerAddress)
	output = strings.ReplaceAll(output, "{domain}", s.config.Domain)
	output = strings.ReplaceAll(output, "{organization}", s.config.Organization)
	return output
}

// GeneratePreseedFile creates a Debian/Ubuntu preseed configuration
func (s *Service) GeneratePreseedFile(distro string) error {
	preseedPath := filepath.Join(s.preseedDir, distro+".cfg")

	var preseed strings.Builder

	// Debian/Ubuntu automated installation preseed
	preseed.WriteString("# CASDC Automated Installation Preseed\n")
	preseed.WriteString(fmt.Sprintf("# Organization: %s\n", s.config.Organization))
	preseed.WriteString("# Generated automatically\n\n")

	// Localization
	preseed.WriteString("d-i debian-installer/locale string en_US.UTF-8\n")
	preseed.WriteString("d-i keyboard-configuration/xkb-keymap select us\n\n")

	// Network configuration
	preseed.WriteString("d-i netcfg/choose_interface select auto\n")
	preseed.WriteString(fmt.Sprintf("d-i netcfg/get_domain string %s\n", s.config.Domain))
	preseed.WriteString("d-i netcfg/wireless_wep string\n\n")

	// Mirror configuration
	preseed.WriteString("d-i mirror/country string manual\n")
	preseed.WriteString("d-i mirror/http/hostname string archive.ubuntu.com\n")
	preseed.WriteString("d-i mirror/http/directory string /ubuntu\n")
	preseed.WriteString("d-i mirror/http/proxy string\n\n")

	// Partitioning
	preseed.WriteString("d-i partman-auto/method string regular\n")
	preseed.WriteString("d-i partman-auto/choose_recipe select atomic\n")
	preseed.WriteString("d-i partman/confirm_write_new_label boolean true\n")
	preseed.WriteString("d-i partman/choose_partition select finish\n")
	preseed.WriteString("d-i partman/confirm boolean true\n")
	preseed.WriteString("d-i partman/confirm_nooverwrite boolean true\n\n")

	// User setup
	preseed.WriteString("d-i passwd/make-user boolean true\n")
	preseed.WriteString("d-i passwd/user-fullname string CASDC Admin\n")
	preseed.WriteString("d-i passwd/username string casdcadmin\n")
	preseed.WriteString("d-i passwd/user-password-crypted password !\n") // Disabled password, SSH key only
	preseed.WriteString("d-i user-setup/allow-password-weak boolean false\n\n")

	// Package selection
	preseed.WriteString("tasksel tasksel/first multiselect standard, ssh-server\n")
	preseed.WriteString("d-i pkgsel/include string openssh-server curl wget vim\n")
	preseed.WriteString("d-i pkgsel/upgrade select full-upgrade\n\n")

	// Boot loader
	preseed.WriteString("d-i grub-installer/only_debian boolean true\n")
	preseed.WriteString("d-i grub-installer/bootdev string default\n\n")

	// Finish
	preseed.WriteString("d-i finish-install/reboot_in_progress note\n")

	// Late command - join CASDC domain
	preseed.WriteString(fmt.Sprintf("d-i preseed/late_command string \\\n"))
	preseed.WriteString(fmt.Sprintf("  curl -sSL http://%s/install/join.sh | chroot /target /bin/bash\n", s.config.ServerAddress))

	if err := os.WriteFile(preseedPath, []byte(preseed.String()), 0644); err != nil {
		return fmt.Errorf("failed to write preseed file: %w", err)
	}

	return nil
}

// GenerateKickstartFile creates a RHEL/CentOS kickstart configuration
func (s *Service) GenerateKickstartFile(distro string) error {
	kickstartPath := filepath.Join(s.kickstartDir, distro+".cfg")

	var kickstart strings.Builder

	// RHEL/CentOS/Fedora automated installation kickstart
	kickstart.WriteString("# CASDC Automated Installation Kickstart\n")
	kickstart.WriteString(fmt.Sprintf("# Organization: %s\n", s.config.Organization))
	kickstart.WriteString("# Generated automatically\n\n")

	kickstart.WriteString("# Install mode\n")
	kickstart.WriteString("install\n")
	kickstart.WriteString("text\n")
	kickstart.WriteString("reboot\n\n")

	// Network
	kickstart.WriteString("network --bootproto=dhcp --activate\n\n")

	// Language and keyboard
	kickstart.WriteString("lang en_US.UTF-8\n")
	kickstart.WriteString("keyboard us\n")
	kickstart.WriteString("timezone UTC\n\n")

	// Authentication
	kickstart.WriteString("authselect select sssd with-mkhomedir\n")
	kickstart.WriteString("rootpw --iscrypted !\n") // Disabled root password
	kickstart.WriteString("user --name=casdcadmin --groups=wheel\n\n")

	// Disk partitioning
	kickstart.WriteString("zerombr\n")
	kickstart.WriteString("clearpart --all --initlabel\n")
	kickstart.WriteString("autopart\n\n")

	// Boot loader
	kickstart.WriteString("bootloader --location=mbr\n\n")

	// Packages
	kickstart.WriteString("%packages\n")
	kickstart.WriteString("@^server-product-environment\n")
	kickstart.WriteString("openssh-server\n")
	kickstart.WriteString("curl\n")
	kickstart.WriteString("wget\n")
	kickstart.WriteString("vim\n")
	kickstart.WriteString("%end\n\n")

	// Post-installation script
	kickstart.WriteString("%post --log=/root/ks-post.log\n")
	kickstart.WriteString(fmt.Sprintf("curl -sSL http://%s/install/join.sh | bash\n", s.config.ServerAddress))
	kickstart.WriteString("%end\n")

	if err := os.WriteFile(kickstartPath, []byte(kickstart.String()), 0644); err != nil {
		return fmt.Errorf("failed to write kickstart file: %w", err)
	}

	return nil
}

// NewTFTPServer creates a new TFTP server instance
func NewTFTPServer(rootDir string, port int, log *logger.Logger) (*TFTPServer, error) {
	return &TFTPServer{
		address:  "0.0.0.0",
		port:     port,
		rootDir:  rootDir,
		logger:   log,
		stopChan: make(chan struct{}),
	}, nil
}

// NewHTTPServer creates a new HTTP server for installation files
func NewHTTPServer(rootDir string, port int, log *logger.Logger) *HTTPServer {
	return &HTTPServer{
		address: "0.0.0.0",
		port:    port,
		rootDir: rootDir,
		logger:  log,
	}
}

// Start starts the TFTP server
func (t *TFTPServer) Start() error {
	addr := fmt.Sprintf("%s:%d", t.address, t.port)
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return fmt.Errorf("failed to resolve TFTP address: %w", err)
	}

	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("failed to start TFTP server: %w", err)
	}

	t.conn = conn
	t.logger.Info("TFTP server started on %s", addr)

	// TODO: Implement TFTP protocol handling
	return nil
}

// Start starts the HTTP server for large file downloads
func (h *HTTPServer) Start() error {
	// TODO: Implement HTTP server for installation files
	h.logger.Info("HTTP server started on %s:%d", h.address, h.port)
	return nil
}

// Shutdown gracefully stops the PXE service
func (s *Service) Shutdown(ctx context.Context) error {
	s.logger.Info("Shutting down PXE boot service")

	if s.tftpServer != nil && s.tftpServer.conn != nil {
		s.tftpServer.conn.Close()
	}

	return nil
}
