package main

import "testing"

func TestIsDomainHandled(t *testing.T) {
	s := &DNSServer{customDomains: []string{"loc", "spark.dev"}}

	tests := []struct {
		name   string
		domain string
		want   bool
	}{
		{"tld exact", "loc.", true},
		{"tld subdomain", "app.loc.", true},
		{"tld deep subdomain", "api.app.loc.", true},
		{"case insensitive", "APP.LOC.", true},
		{"specific domain exact", "spark.dev.", true},
		{"specific domain subdomain", "api.spark.dev.", true},
		{"unrelated", "example.com.", false},
		{"partial suffix not matched", "notspark.dev.", false},
		{"substring not matched", "myloc.", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := s.isDomainHandled(tt.domain); got != tt.want {
				t.Errorf("isDomainHandled(%q) = %v, want %v", tt.domain, got, tt.want)
			}
		})
	}
}
