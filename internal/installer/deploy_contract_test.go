package installer

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repositoryFile(t *testing.T, parts ...string) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	return filepath.Join(append([]string{root}, parts...)...)
}

func readRepositoryFile(t *testing.T, parts ...string) string {
	t.Helper()
	path := repositoryFile(t, parts...)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

func TestDeployInstallerUsesPrivateTemporaryDirectory(t *testing.T) {
	body := readRepositoryFile(t, "deploy", "install.sh")
	for _, forbidden := range []string{"/tmp/gpn_gh_probe", "exec sudo", "will attempt a rootless install"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("deploy/install.sh still contains unsafe contract %q", forbidden)
		}
	}
	for _, required := range []string{
		`mktemp -d "${staging_parent%/}/5gpn-install.XXXXXX"`,
		`chmod 700 "$tmpdir"`,
		`cleanup() { rm -rf -- "$tmpdir"; }`,
		"GPN_PREFIX is not supported by the one-shot production installer",
		"For an unprivileged development boot",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("deploy/install.sh missing %q", required)
		}
	}
}

func TestDeployHasNoDeadIOSSocketUnits(t *testing.T) {
	for _, name := range []string{"5gpn-ios.service", "5gpn-ios.socket"} {
		path := repositoryFile(t, "deploy", "systemd", name)
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("obsolete unit %s must be absent, stat err=%v", path, err)
		}
	}
}

func TestReferenceUnitUsesPrivateStateModes(t *testing.T) {
	body := readRepositoryFile(t, "deploy", "systemd", "5gpn.service")
	for _, required := range []string{
		"StateDirectoryMode=0700",
		"RuntimeDirectoryMode=0700",
		"LogsDirectoryMode=0700",
		"UMask=0077",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("reference unit missing %q", required)
		}
	}
	if strings.Contains(body, "Type=notify") {
		t.Error("reference unit cannot use Type=notify: the daemon does not send sd_notify readiness")
	}
	if !strings.Contains(body, "Type=simple") {
		t.Error("reference unit must use Type=simple")
	}
}
