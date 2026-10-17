package guard

import (
	"os"
	"testing"
)

func TestConfigRejectsInvalidPortsAndCountryCodes(t *testing.T) {
	c := testConfig(t)
	for _, listen := range []string{"127.0.0.1:", "127.0.0.1:http", "127.0.0.1:00", "127.0.0.1:+80", "127.0.0.1:65536", "[::1]:-1"} {
		t.Run(listen, func(t *testing.T) {
			invalid := c
			invalid.Listen = listen
			if invalid.validate() == nil {
				t.Fatalf("invalid listener accepted: %s", listen)
			}
		})
	}
	for _, upstream := range []string{"http://:7897", "http://localhost:", "http://localhost:0", "http://localhost:65536", "http://localhost?"} {
		t.Run(upstream, func(t *testing.T) {
			invalid := c
			invalid.Upstream = upstream
			if invalid.validate() == nil {
				t.Fatalf("invalid upstream accepted: %s", upstream)
			}
		})
	}
	for _, country := range []string{"12", "S1", "sg", "中"} {
		invalid := c
		invalid.ExpectedCountry = country
		if invalid.validate() == nil {
			t.Fatalf("invalid country accepted: %s", country)
		}
	}
}

func TestConfigDetectsImplicitAndPaddedProxyLoops(t *testing.T) {
	c := testConfig(t)
	for _, pair := range [][2]string{
		{"127.0.0.1:80", "http://localhost"},
		{"[::1]:443", "https://127.0.0.1"},
		{"127.0.0.1:17897", "http://localhost:017897"},
		{"127.0.0.1:17897", "http://LOCALHOST.:17897"},
	} {
		c.Listen, c.Upstream = pair[0], pair[1]
		if c.validate() == nil {
			t.Fatalf("proxy loop accepted: %s -> %s", c.Listen, c.Upstream)
		}
	}
}

func TestLoadConfigRejectsTrailingJSON(t *testing.T) {
	c := testConfig(t)
	if e := writeJSON(paths().Config, c); e != nil {
		t.Fatal(e)
	}
	valid, e := os.ReadFile(paths().Config)
	if e != nil {
		t.Fatal(e)
	}
	for _, suffix := range []string{"{}", "null", "trailing garbage"} {
		if e = os.WriteFile(paths().Config, append(append([]byte{}, valid...), suffix...), 0600); e != nil {
			t.Fatal(e)
		}
		if _, e = loadConfig(); e == nil {
			t.Fatalf("trailing input accepted: %s", suffix)
		}
	}
	if e = os.WriteFile(paths().Config, append(valid, '\n', '\t'), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = loadConfig(); e != nil {
		t.Fatalf("trailing whitespace rejected: %v", e)
	}
}
