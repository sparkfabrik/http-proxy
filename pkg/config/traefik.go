package config

// TraefikConfig represents the structure for Traefik dynamic configuration
type TraefikConfig struct {
	HTTP *HTTPConfig `yaml:"http,omitempty"`
	TLS  *TLSConfig  `yaml:"tls,omitempty"`
}

// HTTPConfig represents HTTP configuration
type HTTPConfig struct {
	Routers     map[string]*Router     `yaml:"routers,omitempty"`
	Services    map[string]*Service    `yaml:"services,omitempty"`
	Middlewares map[string]*Middleware `yaml:"middlewares,omitempty"`
}

// Router represents a Traefik router configuration
type Router struct {
	Rule        string           `yaml:"rule,omitempty"`
	Service     string           `yaml:"service,omitempty"`
	EntryPoints []string         `yaml:"entryPoints,omitempty"`
	Middlewares []string         `yaml:"middlewares,omitempty"`
	TLS         *RouterTLSConfig `yaml:"tls,omitempty"`
}

// RouterTLSConfig represents TLS configuration for a router
type RouterTLSConfig struct {
	// Empty struct enables TLS with auto-generated certificates
}

// Middleware represents a Traefik middleware configuration
type Middleware struct {
	Headers *HeadersMiddleware `yaml:"headers,omitempty"`
}

// HeadersMiddleware represents headers middleware configuration
type HeadersMiddleware struct {
	AccessControlAllowCredentials *bool             `yaml:"accessControlAllowCredentials,omitempty"`
	AccessControlAllowHeaders     []string          `yaml:"accessControlAllowHeaders,omitempty"`
	AccessControlAllowMethods     []string          `yaml:"accessControlAllowMethods,omitempty"`
	AccessControlAllowOriginList  []string          `yaml:"accessControlAllowOriginList,omitempty"`
	AccessControlMaxAge           *int64            `yaml:"accessControlMaxAge,omitempty"`
	CustomRequestHeaders          map[string]string `yaml:"customRequestHeaders,omitempty"`
	CustomResponseHeaders         map[string]string `yaml:"customResponseHeaders,omitempty"`
}

// Service represents a Traefik service configuration
type Service struct {
	LoadBalancer *LoadBalancer `yaml:"loadBalancer,omitempty"`
}

// LoadBalancer represents a load balancer configuration
type LoadBalancer struct {
	Servers []Server `yaml:"servers,omitempty"`
}

// Server represents a server configuration
type Server struct {
	URL string `yaml:"url,omitempty"`
}

// TLSConfig represents TLS configuration for certificates
type TLSConfig struct {
	Certificates []TLSCertificate `yaml:"certificates,omitempty"`
}

// TLSCertificate represents a TLS certificate configuration
type TLSCertificate struct {
	CertFile string `yaml:"certFile,omitempty"`
	KeyFile  string `yaml:"keyFile,omitempty"`
}

// NewTraefikConfig creates a new Traefik configuration
func NewTraefikConfig() *TraefikConfig {
	return &TraefikConfig{
		HTTP: &HTTPConfig{
			Routers:     make(map[string]*Router),
			Services:    make(map[string]*Service),
			Middlewares: make(map[string]*Middleware),
		},
	}
}
