// Package api provides REST API functionality for CASDC
package api

import (
	"github.com/casapps/casdc/internal/auth"
	"github.com/casapps/casdc/internal/backup"
	"github.com/casapps/casdc/internal/certificates"
	"github.com/casapps/casdc/internal/cluster"
	"github.com/casapps/casdc/internal/config"
	"github.com/casapps/casdc/internal/database"
	"github.com/casapps/casdc/internal/dhcp"
	"github.com/casapps/casdc/internal/dns"
	"github.com/casapps/casdc/internal/email"
	"github.com/casapps/casdc/internal/git"
	"github.com/casapps/casdc/internal/gpo"
	"github.com/casapps/casdc/internal/ldap"
	"github.com/casapps/casdc/internal/ou"
	"github.com/casapps/casdc/internal/pxe"
	"github.com/casapps/casdc/internal/registry"
	"github.com/casapps/casdc/internal/samba"
	"github.com/casapps/casdc/internal/security"
	"github.com/casapps/casdc/internal/vpn"
	"github.com/casapps/casdc/pkg/logger"
)

// Server represents the REST API server with all CASDC services
type Server struct {
	db           *database.DB
	auth         *auth.Service
	security     *security.Service
	dns          *dns.Service
	email        *email.Service
	certificates *certificates.Service
	backup       *backup.Service
	cluster      *cluster.Service
	dhcp         *dhcp.Service
	samba        *samba.Service
	vpn          *vpn.Service
	pxe          *pxe.Service
	git          *git.Service
	registry     *registry.Service
	ou           *ou.Service
	gpo          *gpo.Service
	ldap         *ldap.Service
	config       *config.Config
	logger       *logger.Logger
}

// NewServer creates a new API server with all service dependencies
func NewServer(
	db *database.DB,
	authService *auth.Service,
	securityService *security.Service,
	dnsService *dns.Service,
	emailService *email.Service,
	certificateService *certificates.Service,
	backupService *backup.Service,
	clusterService *cluster.Service,
	dhcpService *dhcp.Service,
	sambaService *samba.Service,
	vpnService *vpn.Service,
	pxeService *pxe.Service,
	gitService *git.Service,
	registryService *registry.Service,
	ouService *ou.Service,
	gpoService *gpo.Service,
	ldapService *ldap.Service,
	cfg *config.Config,
	log *logger.Logger,
) *Server {
	return &Server{
		db:           db,
		auth:         authService,
		security:     securityService,
		dns:          dnsService,
		email:        emailService,
		certificates: certificateService,
		backup:       backupService,
		cluster:      clusterService,
		dhcp:         dhcpService,
		samba:        sambaService,
		vpn:          vpnService,
		pxe:          pxeService,
		git:          gitService,
		registry:     registryService,
		ou:           ouService,
		gpo:          gpoService,
		ldap:         ldapService,
		config:       cfg,
		logger:       log,
	}
}