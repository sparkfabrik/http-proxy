package utils

import "testing"

func TestGetDockerEnvVar(t *testing.T) {
	env := []string{"FOO=bar", "VIRTUAL_HOST=app.loc", "EMPTY="}
	tests := []struct {
		key  string
		want string
	}{
		{"VIRTUAL_HOST", "app.loc"},
		{"FOO", "bar"},
		{"EMPTY", ""},
		{"MISSING", ""},
	}
	for _, tt := range tests {
		if got := GetDockerEnvVar(env, tt.key); got != tt.want {
			t.Errorf("GetDockerEnvVar(%q) = %q, want %q", tt.key, got, tt.want)
		}
	}
}

func TestHasTraefikLabel(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"nil", nil, false},
		{"none", map[string]string{"com.example": "x"}, false},
		{"enable", map[string]string{"traefik.enable": "true"}, true},
		{"nested", map[string]string{"traefik.http.routers.a.rule": "Host(`x`)"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasTraefikLabel(tt.labels); got != tt.want {
				t.Errorf("HasTraefikLabel(%v) = %v, want %v", tt.labels, got, tt.want)
			}
		})
	}
}

func TestShouldManageContainer(t *testing.T) {
	tests := []struct {
		name   string
		env    []string
		labels map[string]string
		want   bool
	}{
		{"neither", []string{"FOO=bar"}, nil, false},
		{"virtual host", []string{"VIRTUAL_HOST=app.loc"}, nil, true},
		{"traefik label", nil, map[string]string{"traefik.enable": "true"}, true},
		{"both", []string{"VIRTUAL_HOST=app.loc"}, map[string]string{"traefik.enable": "true"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldManageContainer(tt.env, tt.labels); got != tt.want {
				t.Errorf("ShouldManageContainer = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFormatDockerID(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"abcdef0123456789", "abcdef012345"},
		{"short", "short"},
		{"", ""},
		{"abcdef012345", "abcdef012345"},
	}
	for _, tt := range tests {
		if got := FormatDockerID(tt.in); got != tt.want {
			t.Errorf("FormatDockerID(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestValidateLogLevel(t *testing.T) {
	for _, lvl := range []string{"debug", "info", "warn", "error"} {
		if err := ValidateLogLevel(lvl); err != nil {
			t.Errorf("ValidateLogLevel(%q) returned error: %v", lvl, err)
		}
	}
	for _, lvl := range []string{"", "trace", "INFO", "warning"} {
		if err := ValidateLogLevel(lvl); err == nil {
			t.Errorf("ValidateLogLevel(%q) expected error, got nil", lvl)
		}
	}
}
