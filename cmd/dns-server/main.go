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
	c := dns.Client{Timeout: 5 * time.Second}

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
	for _, question := range r.Question {
		name := strings.ToLower(question.Name)

		s.logger.Debug(fmt.Sprintf("DNS query: %s %s from %s",
			dns.TypeToString[question.Qtype],
			name,
			w.RemoteAddr()))

		// Check if domain matches any configured domain/TLD
		// Support both TLDs (e.g., "loc") and specific domains (e.g., "spark.loc")
		domainWithoutDot := strings.TrimSuffix(name, ".")
		found := false

		for _, domain := range s.customDomains {
			// Check if it's an exact match or a subdomain
			if domainWithoutDot == domain || strings.HasSuffix(domainWithoutDot, "."+domain) {
				found = true
				break
			}
		}

		if !found {
			// Not our domain - handle based on forwarding configuration
			if s.forwardEnabled {
				// Forward to upstream DNS servers
				s.logger.Debug("Forwarding query to upstream servers", "name", name)
				response, err := s.forwardDNSQuery(r)
				if err != nil {
					s.logger.Debug(fmt.Sprintf("Failed to forward query for %s: %v", name, err))
					// If forwarding fails, return REFUSED
					refusedResp := s.createRefusedResponse(r)
					w.WriteMsg(refusedResp)
				} else {
					w.WriteMsg(response)
				}
			} else {
				// Forwarding disabled - send REFUSED response
				s.logger.Debug(fmt.Sprintf("Sending REFUSED response for %s (not matching configured domains)", name))
				refusedResp := s.createRefusedResponse(r)
				w.WriteMsg(refusedResp)
			}
			return
		}
	}

	// If we reach here, all queries are for our TLD - create response
	msg := dns.Msg{}
	msg.SetReply(r)
	msg.Authoritative = true

	for _, question := range r.Question {
		name := strings.ToLower(question.Name)

		switch question.Qtype {
		case dns.TypeA:
			// Respond with our target IP for A records
			rr, err := dns.NewRR(fmt.Sprintf("%s A %s", question.Name, s.targetIP))
			if err == nil {
				msg.Answer = append(msg.Answer, rr)
				s.logger.Info(fmt.Sprintf("Resolved %s to %s", name, s.targetIP))
			} else {
				s.logger.Error(fmt.Sprintf("Failed to create A record for %s: %v", name, err))
			}
		case dns.TypeAAAA:
			// For IPv6 queries, return empty response (no IPv6 support)
			s.logger.Debug(fmt.Sprintf("IPv6 query for %s - returning empty response", name))
		default:
			// For other query types, return empty response
			s.logger.Debug(fmt.Sprintf("Unsupported query type %s for %s", dns.TypeToString[question.Qtype], name))
		}
	}

	w.WriteMsg(&msg)
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
