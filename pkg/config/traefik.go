package config

// TraefikConfig represents the structure for Traefik dynamic configuration
type TraefikConfig struct {
	HTTP *HTTPConfig `yaml:"http,omitempty"`
}

// HTTPConfig represents HTTP configuration
type HTTPConfig struct {
	Routers  map[string]*Router  `yaml:"routers,omitempty"`
	Services map[string]*Service `yaml:"services,omitempty"`
}

// Router represents a Traefik router configuration
type Router struct {
	Rule        string   `yaml:"rule,omitempty"`
	Service     string   `yaml:"service,omitempty"`
	EntryPoints []string `yaml:"entryPoints,omitempty"`
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

// NewTraefikConfig creates a new Traefik configuration
func NewTraefikConfig() *TraefikConfig {
	return &TraefikConfig{
		HTTP: &HTTPConfig{
			Routers:  make(map[string]*Router),
			Services: make(map[string]*Service),
		},
	}
}
