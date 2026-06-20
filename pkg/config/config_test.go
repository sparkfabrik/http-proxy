package config

import (
	"reflect"
	"testing"
)

func TestGetEnvOrDefault(t *testing.T) {
	t.Run("returns default when unset", func(t *testing.T) {
		if got := GetEnvOrDefault("HTTP_PROXY_TEST_UNSET", "fallback"); got != "fallback" {
			t.Errorf("got %q, want fallback", got)
		}
	})
	t.Run("returns value when set", func(t *testing.T) {
		t.Setenv("HTTP_PROXY_TEST_SET", "value")
		if got := GetEnvOrDefault("HTTP_PROXY_TEST_SET", "fallback"); got != "value" {
			t.Errorf("got %q, want value", got)
		}
	})
	t.Run("returns default when empty", func(t *testing.T) {
		t.Setenv("HTTP_PROXY_TEST_EMPTY", "")
		if got := GetEnvOrDefault("HTTP_PROXY_TEST_EMPTY", "fallback"); got != "fallback" {
			t.Errorf("got %q, want fallback", got)
		}
	})
}

func TestGetEnvOrDefaultStringSlice(t *testing.T) {
	def := []string{"loc"}

	t.Run("default when unset", func(t *testing.T) {
		got := GetEnvOrDefaultStringSlice("HTTP_PROXY_TEST_SLICE_UNSET", def)
		if !reflect.DeepEqual(got, def) {
			t.Errorf("got %v, want %v", got, def)
		}
	})

	t.Run("splits and trims", func(t *testing.T) {
		t.Setenv("HTTP_PROXY_TEST_SLICE", " loc , dev ,test")
		got := GetEnvOrDefaultStringSlice("HTTP_PROXY_TEST_SLICE", def)
		want := []string{"loc", "dev", "test"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("drops empty entries", func(t *testing.T) {
		t.Setenv("HTTP_PROXY_TEST_SLICE_EMPTY", "loc,,dev,")
		got := GetEnvOrDefaultStringSlice("HTTP_PROXY_TEST_SLICE_EMPTY", def)
		want := []string{"loc", "dev"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("default when only separators", func(t *testing.T) {
		t.Setenv("HTTP_PROXY_TEST_SLICE_SEP", " , , ")
		got := GetEnvOrDefaultStringSlice("HTTP_PROXY_TEST_SLICE_SEP", def)
		if !reflect.DeepEqual(got, def) {
			t.Errorf("got %v, want %v", got, def)
		}
	})
}
