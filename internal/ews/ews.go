// Package ews implements Microsoft Exchange Web Services (EWS) protocol
// providing SOAP-based API for third-party application integration with calendar,
// free/busy information, advanced search, and bulk operations
package ews

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/casapps/casdc/internal/config"
	"github.com/casapps/casdc/internal/database"
	"github.com/casapps/casdc/pkg/logger"
)

// Service handles Exchange Web Services (EWS) protocol operations
// including calendar free/busy, search, and bulk operations for third-party integration
type Service struct {
	db     *database.DB
	config *config.Config
	logger *logger.Logger

	// EWS settings
	enabled                    bool
	maxConcurrentConnections   int
	maxRequestSize             int64
	throttlingEnabled          bool
	throttlingMaxRequestsPerMin int
	impersonationEnabled       bool

	// Request tracking for throttling
	requestCounts      map[string]*requestCounter
	requestCountsMutex sync.RWMutex
}

// requestCounter tracks requests per IP for throttling
type requestCounter struct {
	count     int
	resetTime time.Time
}

// EWS SOAP envelope structures for request/response handling
type SOAPEnvelope struct {
	XMLName xml.Name    `xml:"http://schemas.xmlsoap.org/soap/envelope/ Envelope"`
	Header  *SOAPHeader `xml:"Header,omitempty"`
	Body    SOAPBody    `xml:"Body"`
}

// SOAPHeader contains authentication and impersonation information
type SOAPHeader struct {
	XMLName              xml.Name              `xml:"http://schemas.xmlsoap.org/soap/envelope/ Header"`
	ExchangeImpersonation *ExchangeImpersonation `xml:"ExchangeImpersonation,omitempty"`
	RequestServerVersion *RequestServerVersion  `xml:"RequestServerVersion,omitempty"`
}

// SOAPBody contains the actual EWS operation request or response
type SOAPBody struct {
	XMLName xml.Name    `xml:"http://schemas.xmlsoap.org/soap/envelope/ Body"`
	Content interface{} `xml:",any"`
}

// ExchangeImpersonation allows service accounts to act as other users
type ExchangeImpersonation struct {
	XMLName              xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types ExchangeImpersonation"`
	ConnectingSID        ConnectingSID `xml:"ConnectingSID"`
}

// ConnectingSID identifies the user to impersonate
type ConnectingSID struct {
	PrimarySmtpAddress string `xml:"PrimarySmtpAddress,omitempty"`
	SID                string `xml:"SID,omitempty"`
	PrincipalName      string `xml:"PrincipalName,omitempty"`
}

// RequestServerVersion specifies the EWS schema version
type RequestServerVersion struct {
	XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/services/2006/types RequestServerVersion"`
	Version string   `xml:"Version,attr"`
}

// GetFolder request to retrieve folder information
type GetFolderRequest struct {
	XMLName     xml.Name    `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetFolder"`
	FolderShape FolderShape `xml:"FolderShape"`
	FolderIds   FolderIds   `xml:"FolderIds"`
}

// FolderShape specifies what properties to return
type FolderShape struct {
	BaseShape string `xml:"http://schemas.microsoft.com/exchange/services/2006/types BaseShape"`
}

// FolderIds contains the folders to retrieve
type FolderIds struct {
	DistinguishedFolderId []DistinguishedFolderId `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId,omitempty"`
	FolderId              []FolderId              `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId,omitempty"`
}

// DistinguishedFolderId represents well-known folders (inbox, calendar, etc.)
type DistinguishedFolderId struct {
	Id      string   `xml:"Id,attr"`
	Mailbox *Mailbox `xml:"http://schemas.microsoft.com/exchange/services/2006/types Mailbox,omitempty"`
}

// FolderId represents a specific folder by ID
type FolderId struct {
	Id        string `xml:"Id,attr"`
	ChangeKey string `xml:"ChangeKey,attr,omitempty"`
}

// Mailbox identifies a mailbox for folder operations
type Mailbox struct {
	EmailAddress string `xml:"EmailAddress"`
}

// FindItem request to search for items in folders
type FindItemRequest struct {
	XMLName       xml.Name      `xml:"http://schemas.microsoft.com/exchange/services/2006/messages FindItem"`
	Traversal     string        `xml:"Traversal,attr"`
	ItemShape     ItemShape     `xml:"ItemShape"`
	ParentFolderIds ParentFolderIds `xml:"ParentFolderIds"`
	Restriction   *Restriction  `xml:"Restriction,omitempty"`
}

// ItemShape specifies what item properties to return
type ItemShape struct {
	BaseShape            string `xml:"http://schemas.microsoft.com/exchange/services/2006/types BaseShape"`
	IncludeMimeContent   bool   `xml:"http://schemas.microsoft.com/exchange/services/2006/types IncludeMimeContent,omitempty"`
	BodyType             string `xml:"http://schemas.microsoft.com/exchange/services/2006/types BodyType,omitempty"`
	AdditionalProperties []AdditionalProperty `xml:"http://schemas.microsoft.com/exchange/services/2006/types AdditionalProperties>Path,omitempty"`
}

// AdditionalProperty specifies extra properties to include
type AdditionalProperty struct {
	FieldURI string `xml:"FieldURI,attr"`
}

// ParentFolderIds specifies which folders to search
type ParentFolderIds struct {
	DistinguishedFolderId []DistinguishedFolderId `xml:"http://schemas.microsoft.com/exchange/services/2006/types DistinguishedFolderId,omitempty"`
	FolderId              []FolderId              `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId,omitempty"`
}

// Restriction contains search filters
type Restriction struct {
	XMLName xml.Name    `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Restriction"`
	Content interface{} `xml:",any"`
}

// GetUserAvailability request for calendar free/busy information
type GetUserAvailabilityRequest struct {
	XMLName    xml.Name   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages GetUserAvailabilityRequest"`
	TimeZone   TimeZone   `xml:"http://schemas.microsoft.com/exchange/services/2006/types TimeZone"`
	MailboxDataArray MailboxDataArray `xml:"MailboxDataArray"`
	FreeBusyViewOptions FreeBusyViewOptions `xml:"FreeBusyViewOptions"`
}

// TimeZone specifies the timezone for availability requests
type TimeZone struct {
	Bias               int    `xml:"Bias"`
	StandardTime       Time   `xml:"StandardTime"`
	DaylightTime       Time   `xml:"DaylightTime"`
}

// Time represents a timezone transition time
type Time struct {
	Bias      int    `xml:"Bias"`
	Time      string `xml:"Time"`
	DayOrder  int    `xml:"DayOrder"`
	Month     int    `xml:"Month"`
	DayOfWeek string `xml:"DayOfWeek"`
}

// MailboxDataArray contains mailboxes to check availability
type MailboxDataArray struct {
	MailboxData []MailboxData `xml:"http://schemas.microsoft.com/exchange/services/2006/types MailboxData"`
}

// MailboxData identifies a mailbox for availability checking
type MailboxData struct {
	Email           Email  `xml:"Email"`
	AttendeeType    string `xml:"AttendeeType"`
	ExcludeConflicts bool   `xml:"ExcludeConflicts,omitempty"`
}

// Email represents an email address
type Email struct {
	Address string `xml:"Address"`
}

// FreeBusyViewOptions specifies the time range and detail level
type FreeBusyViewOptions struct {
	TimeWindow           TimeWindow `xml:"http://schemas.microsoft.com/exchange/services/2006/types TimeWindow"`
	MergedFreeBusyIntervalInMinutes int `xml:"http://schemas.microsoft.com/exchange/services/2006/types MergedFreeBusyIntervalInMinutes"`
	RequestedView        string     `xml:"http://schemas.microsoft.com/exchange/services/2006/types RequestedView"`
}

// TimeWindow specifies the start and end time for availability
type TimeWindow struct {
	StartTime string `xml:"StartTime"`
	EndTime   string `xml:"EndTime"`
}

// NewService creates a new EWS service instance with database and configuration
func NewService(db *database.DB, cfg *config.Config, log *logger.Logger) (*Service, error) {
	s := &Service{
		db:     db,
		config: cfg,
		logger: log,

		// Default EWS settings
		enabled:                    true,
		maxConcurrentConnections:   100,
		maxRequestSize:             10 * 1024 * 1024, // 10MB
		throttlingEnabled:          true,
		throttlingMaxRequestsPerMin: 60,
		impersonationEnabled:       false,

		requestCounts: make(map[string]*requestCounter),
	}

	// Load EWS settings from database
	if err := s.loadSettings(); err != nil {
		log.Warn("Failed to load EWS settings, using defaults: %v", err)
	}

	return s, nil
}

// loadSettings retrieves EWS configuration from database
func (s *Service) loadSettings() error {
	query := `SELECT enabled, max_concurrent_connections, max_request_size,
	          throttling_enabled, throttling_max_requests_per_minute, impersonation_enabled
	          FROM ews_settings ORDER BY id DESC LIMIT 1`

	err := s.db.QueryRow(query).Scan(
		&s.enabled,
		&s.maxConcurrentConnections,
		&s.maxRequestSize,
		&s.throttlingEnabled,
		&s.throttlingMaxRequestsPerMin,
		&s.impersonationEnabled,
	)

	if err != nil {
		return fmt.Errorf("failed to load EWS settings: %w", err)
	}

	return nil
}

// HandleRequest processes EWS SOAP requests from clients
func (s *Service) HandleRequest(w http.ResponseWriter, r *http.Request) {
	// Check if EWS is enabled
	if !s.enabled {
		s.sendSOAPFault(w, "ServiceDisabled", "Exchange Web Services is disabled")
		return
	}

	// Check request size limit
	if r.ContentLength > s.maxRequestSize {
		s.sendSOAPFault(w, "RequestTooLarge", fmt.Sprintf("Request size exceeds maximum of %d bytes", s.maxRequestSize))
		return
	}

	// Apply throttling if enabled
	if s.throttlingEnabled {
		if !s.checkThrottle(r.RemoteAddr) {
			s.sendSOAPFault(w, "ErrorServerBusy", "Too many requests, please try again later")
			return
		}
	}

	// Read and parse SOAP request
	body, err := io.ReadAll(io.LimitReader(r.Body, s.maxRequestSize))
	if err != nil {
		s.logger.Error("Failed to read EWS request body: %v", err)
		s.sendSOAPFault(w, "ErrorInvalidRequest", "Failed to read request")
		return
	}

	var envelope SOAPEnvelope
	if err := xml.Unmarshal(body, &envelope); err != nil {
		s.logger.Error("Failed to parse EWS SOAP envelope: %v", err)
		s.sendSOAPFault(w, "ErrorInvalidRequest", "Invalid SOAP request")
		return
	}

	// Route request to appropriate handler based on operation
	s.routeRequest(w, r, &envelope)
}

// checkThrottle verifies if request should be allowed based on rate limiting
func (s *Service) checkThrottle(remoteAddr string) bool {
	s.requestCountsMutex.Lock()
	defer s.requestCountsMutex.Unlock()

	now := time.Now()
	counter, exists := s.requestCounts[remoteAddr]

	if !exists || now.After(counter.resetTime) {
		// Create new counter with 1-minute window
		s.requestCounts[remoteAddr] = &requestCounter{
			count:     1,
			resetTime: now.Add(time.Minute),
		}
		return true
	}

	// Check if under limit
	if counter.count >= s.throttlingMaxRequestsPerMin {
		return false
	}

	// Increment counter
	counter.count++
	return true
}

// routeRequest determines which EWS operation to execute
func (s *Service) routeRequest(w http.ResponseWriter, r *http.Request, envelope *SOAPEnvelope) {
	// Parse the SOAP body to determine operation type
	bodyXML, err := xml.Marshal(envelope.Body.Content)
	if err != nil {
		s.sendSOAPFault(w, "ErrorInvalidRequest", "Failed to process request")
		return
	}

	bodyStr := string(bodyXML)

	// Route based on operation name in SOAP body
	switch {
	case strings.Contains(bodyStr, "GetFolder"):
		s.handleGetFolder(w, r, envelope)
	case strings.Contains(bodyStr, "FindItem"):
		s.handleFindItem(w, r, envelope)
	case strings.Contains(bodyStr, "GetUserAvailability"):
		s.handleGetUserAvailability(w, r, envelope)
	case strings.Contains(bodyStr, "CreateItem"):
		s.handleCreateItem(w, r, envelope)
	case strings.Contains(bodyStr, "UpdateItem"):
		s.handleUpdateItem(w, r, envelope)
	case strings.Contains(bodyStr, "DeleteItem"):
		s.handleDeleteItem(w, r, envelope)
	default:
		s.sendSOAPFault(w, "ErrorNotImplemented", "The requested operation is not implemented")
	}
}

// handleGetFolder retrieves folder information from mailbox
func (s *Service) handleGetFolder(w http.ResponseWriter, r *http.Request, envelope *SOAPEnvelope) {
	// TODO: Implement GetFolder operation
	// This would retrieve folder metadata from the database
	s.logger.Debug("Handling GetFolder request")
	s.sendSOAPFault(w, "ErrorNotImplemented", "GetFolder is not yet implemented")
}

// handleFindItem searches for items in folders
func (s *Service) handleFindItem(w http.ResponseWriter, r *http.Request, envelope *SOAPEnvelope) {
	// TODO: Implement FindItem operation
	// This would search for messages, calendar items, etc.
	s.logger.Debug("Handling FindItem request")
	s.sendSOAPFault(w, "ErrorNotImplemented", "FindItem is not yet implemented")
}

// handleGetUserAvailability retrieves calendar free/busy information
func (s *Service) handleGetUserAvailability(w http.ResponseWriter, r *http.Request, envelope *SOAPEnvelope) {
	// TODO: Implement GetUserAvailability operation
	// This would query calendar data and return free/busy status
	s.logger.Debug("Handling GetUserAvailability request")
	s.sendSOAPFault(w, "ErrorNotImplemented", "GetUserAvailability is not yet implemented")
}

// handleCreateItem creates new items (messages, appointments, etc.)
func (s *Service) handleCreateItem(w http.ResponseWriter, r *http.Request, envelope *SOAPEnvelope) {
	// TODO: Implement CreateItem operation
	s.logger.Debug("Handling CreateItem request")
	s.sendSOAPFault(w, "ErrorNotImplemented", "CreateItem is not yet implemented")
}

// handleUpdateItem updates existing items
func (s *Service) handleUpdateItem(w http.ResponseWriter, r *http.Request, envelope *SOAPEnvelope) {
	// TODO: Implement UpdateItem operation
	s.logger.Debug("Handling UpdateItem request")
	s.sendSOAPFault(w, "ErrorNotImplemented", "UpdateItem is not yet implemented")
}

// handleDeleteItem deletes items from folders
func (s *Service) handleDeleteItem(w http.ResponseWriter, r *http.Request, envelope *SOAPEnvelope) {
	// TODO: Implement DeleteItem operation
	s.logger.Debug("Handling DeleteItem request")
	s.sendSOAPFault(w, "ErrorNotImplemented", "DeleteItem is not yet implemented")
}

// sendSOAPFault sends a SOAP fault response for errors
func (s *Service) sendSOAPFault(w http.ResponseWriter, faultCode, faultString string) {
	fault := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body>
    <soap:Fault>
      <faultcode>%s</faultcode>
      <faultstring>%s</faultstring>
    </soap:Fault>
  </soap:Body>
</soap:Envelope>`, faultCode, faultString)

	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	w.Write([]byte(fault))
}

// Shutdown gracefully stops the EWS service
func (s *Service) Shutdown(ctx context.Context) error {
	s.logger.Info("Shutting down Exchange Web Services")
	// Clean up resources if needed
	return nil
}
