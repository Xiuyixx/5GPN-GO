package installer

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// Distro captures the pieces of /etc/os-release we care about for
// installer routing. Only `ID`, `VERSION_ID`, and `ID_LIKE` are consulted;
// the raw map is kept for diagnostic output.
type Distro struct {
	ID        string            // "ubuntu", "debian", "centos", "almalinux", "rockylinux", "rhel", "fedora"
	VersionID string            // "22.04", "24.04", "12", "9", ...
	IDLike    []string          // parsed from ID_LIKE (e.g. ["rhel", "fedora"])
	Name      string            // pretty NAME
	Raw       map[string]string // full parsed map
}

// Family is the coarse routing bucket. Every action that differs across
// distros (package manager, unit path, systemd generator quirks) branches
// on Family, not on ID.
type Family string

const (
	FamilyDebian  Family = "debian"  // Debian, Ubuntu, and downstreams
	FamilyRHEL    Family = "rhel"    // RHEL, CentOS, AlmaLinux, Rocky, Oracle
	FamilyFedora  Family = "fedora"  // Fedora + IoT variants
	FamilyUnknown Family = "unknown" // caller must decide whether to abort
)

// Family returns the routing bucket for this distro.
func (d Distro) Family() Family {
	switch d.ID {
	case "ubuntu", "debian":
		return FamilyDebian
	case "centos", "almalinux", "rocky", "rockylinux", "rhel", "ol", "oraclelinux":
		return FamilyRHEL
	case "fedora":
		return FamilyFedora
	}
	for _, like := range d.IDLike {
		switch like {
		case "debian":
			return FamilyDebian
		case "rhel", "centos":
			return FamilyRHEL
		case "fedora":
			return FamilyFedora
		}
	}
	return FamilyUnknown
}

// ErrOSReleaseMissing signals that no /etc/os-release could be found and
// no fixture was provided.
var ErrOSReleaseMissing = errors.New("installer: /etc/os-release not found")

// DetectDistro reads /etc/os-release from disk. Returns ErrOSReleaseMissing
// on non-Linux dev machines (macOS, Windows).
func DetectDistro() (Distro, error) {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		if os.IsNotExist(err) {
			return Distro{}, ErrOSReleaseMissing
		}
		return Distro{}, fmt.Errorf("read os-release: %w", err)
	}
	return parseOSRelease(string(b)), nil
}

// LoadOSFixture reads an os-release fixture from a path. Used by tests
// and by `--os-fixture` to preview installer behavior against a specific
// distro/version without provisioning a VM.
func LoadOSFixture(path string) (Distro, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Distro{}, fmt.Errorf("read fixture: %w", err)
	}
	return parseOSRelease(string(b)), nil
}

func parseOSRelease(body string) Distro {
	raw := map[string]string{}
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.IndexByte(line, '=')
		if i <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:i])
		val := strings.TrimSpace(line[i+1:])
		val = strings.Trim(val, `"'`)
		raw[key] = val
	}
	d := Distro{
		ID:        strings.ToLower(raw["ID"]),
		VersionID: raw["VERSION_ID"],
		Name:      raw["NAME"],
		Raw:       raw,
	}
	if v := raw["ID_LIKE"]; v != "" {
		for like := range strings.FieldsSeq(strings.ReplaceAll(v, ",", " ")) {
			d.IDLike = append(d.IDLike, strings.ToLower(like))
		}
	}
	return d
}

// String makes Distro nicer to log.
func (d Distro) String() string {
	if d.ID == "" {
		return "unknown"
	}
	return fmt.Sprintf("%s %s (%s)", d.ID, d.VersionID, d.Family())
}
