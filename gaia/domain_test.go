package gaia

import (
	"strings"
	"testing"
)

// These tests are offline: they exercise the URI driver's pure string functions
// and the host wiring (mint, body, resolve), which need no network. The client's
// HTTP behaviour is covered in gaia_test.go.

func TestDomainInfo(t *testing.T) {
	info := Domain{}.Info()
	if info.Scheme != "gaia" {
		t.Errorf("Scheme = %q, want gaia", info.Scheme)
	}
	if len(info.Hosts) == 0 || info.Hosts[0] != Host {
		t.Errorf("Hosts = %v, want [%s]", info.Hosts, Host)
	}
	if info.Identity.Binary != "gaia" {
		t.Errorf("Identity.Binary = %q, want gaia", info.Identity.Binary)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		in  string
		typ string
		id  string
	}{
		{"137338313799478912", "star", "137338313799478912"},
		{"5853498713190525696", "star", "5853498713190525696"},
		{"some-non-numeric-id", "star", "some-non-numeric-id"},
	}
	for _, tc := range cases {
		typ, id, err := Domain{}.Classify(tc.in)
		if err != nil || typ != tc.typ || id != tc.id {
			t.Errorf("Classify(%q) = (%q, %q, %v), want (%q, %q, nil)",
				tc.in, typ, id, err, tc.typ, tc.id)
		}
	}
}

func TestClassifyEmpty(t *testing.T) {
	_, _, err := Domain{}.Classify("")
	if err == nil {
		t.Error("Classify(\"\") should return an error")
	}
}

func TestLocate(t *testing.T) {
	got, err := Domain{}.Locate("star", "137338313799478912")
	if err != nil {
		t.Fatalf("Locate error: %v", err)
	}
	if !strings.Contains(got, "gea.esac.esa.int") {
		t.Errorf("Locate = %q, want URL containing gea.esac.esa.int", got)
	}
	if !strings.Contains(got, "137338313799478912") {
		t.Errorf("Locate = %q, want URL containing source_id", got)
	}
}

func TestLocateUnknownType(t *testing.T) {
	_, err := Domain{}.Locate("planet", "42")
	if err == nil {
		t.Error("Locate with unknown type should return an error")
	}
}

func TestParseStarIDString(t *testing.T) {
	row := map[string]interface{}{
		"source_id":        float64(137338313799478912),
		"ra":               45.081118730187555,
		"dec":              35.303675774754296,
		"parallax":         0.053,
		"phot_g_mean_mag":  12.5,
		"radial_velocity":  22.5,
	}
	s := parseStar(row)
	if s.ID == "" {
		t.Error("parseStar: ID must not be empty")
	}
	// ID must be a string, not a float.
	if strings.Contains(s.ID, ".") || strings.Contains(s.ID, "e") {
		t.Errorf("parseStar: ID = %q looks like a float, want plain integer string", s.ID)
	}
	if s.RA != 45.081118730187555 {
		t.Errorf("parseStar: RA = %v, want 45.081118730187555", s.RA)
	}
	if s.MagG != 12.5 {
		t.Errorf("parseStar: MagG = %v, want 12.5", s.MagG)
	}
	if s.RadialVel != 22.5 {
		t.Errorf("parseStar: RadialVel = %v, want 22.5", s.RadialVel)
	}
}
