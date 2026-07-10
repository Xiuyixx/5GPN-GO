package installer

import (
	"path/filepath"
	"testing"
)

// Every fixture under testdata/os-release must parse cleanly and route
// to the family recorded here. If a new distro is added, extend both
// the fixture and this table together.
var fixtureCases = []struct {
	Fixture   string
	WantID    string
	WantVer   string
	WantFam   Family
}{
	{"ubuntu-22.04", "ubuntu", "22.04", FamilyDebian},
	{"ubuntu-24.04", "ubuntu", "24.04", FamilyDebian},
	{"debian-12", "debian", "12", FamilyDebian},
	{"debian-13", "debian", "13", FamilyDebian},
	{"centos-9", "centos", "9", FamilyRHEL},
	{"almalinux-9", "almalinux", "9.4", FamilyRHEL},
	{"rocky-9", "rocky", "9.4", FamilyRHEL},
	{"rhel-9", "rhel", "9.4", FamilyRHEL},
	{"fedora-40", "fedora", "40", FamilyFedora},
}

func TestOSFixtures_ParseAndRoute(t *testing.T) {
	for _, tc := range fixtureCases {
		t.Run(tc.Fixture, func(t *testing.T) {
			path := filepath.Join("testdata", "os-release", tc.Fixture)
			d, err := LoadOSFixture(path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if d.ID != tc.WantID {
				t.Errorf("ID = %q, want %q", d.ID, tc.WantID)
			}
			if d.VersionID != tc.WantVer {
				t.Errorf("VersionID = %q, want %q", d.VersionID, tc.WantVer)
			}
			if d.Family() != tc.WantFam {
				t.Errorf("Family() = %q, want %q", d.Family(), tc.WantFam)
			}
			if d.Name == "" {
				t.Errorf("Name empty: %+v", d.Raw)
			}
		})
	}
}

func TestOSDetect_UnknownFamilyIsExplicit(t *testing.T) {
	d := parseOSRelease("ID=someoddlinux\nVERSION_ID=1\nNAME=Odd\n")
	if d.Family() != FamilyUnknown {
		t.Errorf("want FamilyUnknown for unrecognized ID, got %q", d.Family())
	}
}

func TestOSDetect_IDLikeFallback(t *testing.T) {
	// ID unknown but ID_LIKE points at debian → Family should route as debian.
	d := parseOSRelease(`ID=derivative
VERSION_ID=1
ID_LIKE="ubuntu debian"
NAME=Derivative
`)
	if d.Family() != FamilyDebian {
		t.Errorf("ID_LIKE debian should route to FamilyDebian, got %q", d.Family())
	}
	if len(d.IDLike) != 2 || d.IDLike[0] != "ubuntu" {
		t.Errorf("IDLike parse: %v", d.IDLike)
	}
}

func TestOSDetect_QuotesAndCommentsStripped(t *testing.T) {
	d := parseOSRelease(`# leading comment
ID="ubuntu"
VERSION_ID='22.04'
NAME=Ubuntu

ID_LIKE=debian
`)
	if d.ID != "ubuntu" || d.VersionID != "22.04" || d.Name != "Ubuntu" {
		t.Errorf("parse mishandled quotes: %+v", d.Raw)
	}
}
