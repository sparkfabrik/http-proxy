package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/miekg/dns"
	"github.com/sparkfabrik/http-proxy/pkg/config"
	"github.com/sparkfabrik/http-proxy/pkg/logger"
)

// DNS_UPSTREAM_TIMEOUT defines the timeout for DNS queries to upstream servers
const DNS_UPSTREAM_TIMEOUT = 5 * time.Second

type DNSServer struct {
	customDomains   []string
	targetIP        string
	port            string
	forwardEnabled  bool
	upstreamServers []string
	logger          *logger.Logger
}

// forwardDNSQuery forwards DNS queries to upstream servers
func (s *DNSServer) forwardDNSQuery(r *dns.Msg) (*dns.Msg, error) {
	// Basic validation to prevent abuse
	if len(r.Question) == 0 || len(r.Question) > 10 {
		return nil, fmt.Errorf("invalid query: bad question count")
	}

	// Validate each question for security
	for _, question := range r.Question {
		// Validate domain name length (RFC 1034/1035)
		if len(question.Name) > 253 {
			return nil, fmt.Errorf("invalid query: domain name too long")
		}

		// Check for malicious patterns that could cause amplification
		if strings.Count(question.Name, ".") > 127 {
			return nil, fmt.Errorf("invalid query: too many subdomains")
		}
	}

	c := dns.Client{Timeout: DNS_UPSTREAM_TIMEOUT}

	for _, server := range s.upstreamServers {
		resp, _, err := c.Exchange(r, server)
		if err == nil {
			s.logger.Debug("Forwarded query", "server", server)
			return resp, nil
		}
		s.logger.Debug("Failed to forward", "server", server, "error", err)
	}

	return nil, fmt.Errorf("all upstream servers failed")
}

// createRefusedResponse creates a REFUSED response for the given request
func (s *DNSServer) createRefusedResponse(r *dns.Msg) *dns.Msg {
	msg := dns.Msg{}
	msg.SetReply(r)
	msg.Rcode = dns.RcodeRefused
	return &msg
}

// isDomainHandled checks if a domain matches any configured domain/TLD
func (s *DNSServer) isDomainHandled(domain string) bool {
	domainWithoutDot := strings.TrimSuffix(strings.ToLower(domain), ".")

	for _, configuredDomain := range s.customDomains {
		// Check if it's an exact match or a subdomain
		if domainWithoutDot == configuredDomain || strings.HasSuffix(domainWithoutDot, "."+configuredDomain) {
			return true
		}
	}
	return false
}

// validateAllQuestions checks if all questions in the request are for domains we handle
func (s *DNSServer) validateAllQuestions(r *dns.Msg) bool {
	for _, question := range r.Question {
		name := strings.ToLower(question.Name)

		s.logger.Debug("DNS query",
			"type", dns.TypeToString[question.Qtype],
			"name", name)

		if !s.isDomainHandled(name) {
			return false
		}
	}
	return true
}

// handleNonMatchingDomain handles queries for domains we don't manage
func (s *DNSServer) handleNonMatchingDomain(w dns.ResponseWriter, r *dns.Msg) {
	if s.forwardEnabled {
		// Forward to upstream DNS servers
		s.logger.Debug("Forwarding query to upstream servers")
		response, err := s.forwardDNSQuery(r)
		if err != nil {
			s.logger.Debug("Failed to forward query", "error", err)
			// If forwarding fails, return REFUSED
			refusedResp := s.createRefusedResponse(r)
			w.WriteMsg(refusedResp)
		} else {
			w.WriteMsg(response)
		}
	} else {
		// Forwarding disabled - send REFUSED response
		s.logger.Debug("Sending REFUSED response (not matching configured domains)")
		refusedResp := s.createRefusedResponse(r)
		w.WriteMsg(refusedResp)
	}
}

// createARecord creates an A record for the given question
func (s *DNSServer) createARecord(question dns.Question) (dns.RR, error) {
	return dns.NewRR(fmt.Sprintf("%s A %s", question.Name, s.targetIP))
}

// handleQuestion processes a single DNS question and adds answers to the response
func (s *DNSServer) handleQuestion(question dns.Question, msg *dns.Msg) {
	name := strings.ToLower(question.Name)

	switch question.Qtype {
	case dns.TypeA:
		// Respond with our target IP for A records
		rr, err := s.createARecord(question)
		if err == nil {
			msg.Answer = append(msg.Answer, rr)
			s.logger.Info("Resolved A record", "name", name, "ip", s.targetIP)
		} else {
			s.logger.Error("Failed to create A record", "name", name, "error", err)
		}
	case dns.TypeAAAA:
		// For IPv6 queries, return empty response (no IPv6 support)
		s.logger.Debug("IPv6 query - returning empty response", "name", name)
	default:
		// For other query types, return empty response
		s.logger.Debug("Unsupported query type", "type", dns.TypeToString[question.Qtype], "name", name)
	}
}

// createDNSResponse creates a DNS response for queries we handle
func (s *DNSServer) createDNSResponse(r *dns.Msg) *dns.Msg {
	msg := dns.Msg{}
	msg.SetReply(r)
	msg.Authoritative = true

	for _, question := range r.Question {
		s.handleQuestion(question, &msg)
	}

	return &msg
}

// handleDNSRequest processes incoming DNS queries
func (s *DNSServer) handleDNSRequest(w dns.ResponseWriter, r *dns.Msg) {
	// Only respond to queries for our configured domains/TLDs
	// Security: Silently drop queries for domains we're not authoritative for
	// This prevents DNS amplification attacks and reduces information leakage
	if len(s.customDomains) == 0 {
		s.logger.Debug("No custom domains/TLDs configured, dropping query")
		return
	}

	// First, validate that all questions are for domains we handle
	if !s.validateAllQuestions(r) {
		// Handle queries for domains we don't manage
		s.handleNonMatchingDomain(w, r)
		return
	}

	// All queries are for our domains - create and send response
	response := s.createDNSResponse(r)
	w.WriteMsg(response)
}

func main() {
	// Load configuration
	cfg := config.Load()
	log := logger.NewWithEnv("dns-server")

	server := &DNSServer{
		customDomains:   cfg.Domains,
		targetIP:        cfg.DNSIP,
		port:            cfg.DNSPort,
		forwardEnabled:  cfg.DNSForwardEnabled,
		upstreamServers: cfg.DNSUpstreamServers,
		logger:          log,
	}

	if len(server.customDomains) == 0 {
		log.Error("No domains/TLDs configured")
		os.Exit(1)
	}

	// Validate target IP
	if net.ParseIP(cfg.DNSIP) == nil {
		log.Error("Invalid target IP address", "ip", cfg.DNSIP)
		os.Exit(1)
	}

	log.Info("Starting DNS server", "port", cfg.DNSPort)
	log.Info("Handling domains/TLDs", "domains", cfg.Domains)
	log.Info("Resolving to", "target_ip", cfg.DNSIP)
	log.Info("DNS forwarding", "forward_enabled", cfg.DNSForwardEnabled)
	if cfg.DNSForwardEnabled {
		log.Info("DNS upstream servers", "servers", cfg.DNSUpstreamServers)
	}

	// Create DNS server
	dns.HandleFunc(".", server.handleDNSRequest)

	udpServer := &dns.Server{
		Addr:    ":" + cfg.DNSPort,
		Net:     "udp",
		Handler: dns.DefaultServeMux,
	}

	tcpServer := &dns.Server{
		Addr:    ":" + cfg.DNSPort,
		Net:     "tcp",
		Handler: dns.DefaultServeMux,
	}

	// Create error channel for server startup errors
	errChan := make(chan error, 2)

	// Start servers in goroutines
	go func() {
		if err := udpServer.ListenAndServe(); err != nil {
			errChan <- fmt.Errorf("UDP server failed: %v", err)
		}
	}()

	go func() {
		if err := tcpServer.ListenAndServe(); err != nil {
			errChan <- fmt.Errorf("TCP server failed: %v", err)
		}
	}()

	// Check for startup errors
	select {
	case err := <-errChan:
		log.Error("Server startup failed", "error", err)
		os.Exit(1)
	case <-time.After(100 * time.Millisecond):
	}

	log.Info("DNS server started successfully")

	// Wait for interrupt signal
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	log.Info("Shutting down DNS server...")
	udpServer.Shutdown()
	tcpServer.Shutdown()
}
