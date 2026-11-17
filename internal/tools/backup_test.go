package tools

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi/unifitest"
)

func backupEnv(t *testing.T) (*testEnv, string) {
	dir := t.TempDir()
	opts := toolOptions(true, false)
	opts.BackupDir = dir
	env := startMock(t, opts)
	env.mock.
		On("GET", `^/dl/backup/([^/]+)$`, func(_ any, m []string, _ url.Values) (int, any) {
			return 200, unifitest.Raw("manual backup " + m[1])
		}).
		On("GET", `^/dl/autobackup/empty\.unf$`, func(any, []string, url.Values) (int, any) {
			return 200, unifitest.Raw("")
		}).
		On("GET", `^/dl/autobackup/([^/]+)$`, func(_ any, m []string, _ url.Values) (int, any) {
			if m[1] == "missing.unf" {
				return 404, ojson.New("error", "not found")
			}
			return 200, unifitest.Raw("auto backup " + m[1])
		})
	return env, dir
}

func TestDownloadBackup(t *testing.T) {
	env, dir := backupEnv(t)
	res := env.call("unifi_download_backup", `{"source":"/dl/backup/9.0.unf"}`)
	eq(t, "result", res, ojson.New("saved", filepath.Join(dir, "9.0.unf"), "bytes", 21.0, "source", "/dl/backup/9.0.unf"))
	data, err := os.ReadFile(filepath.Join(dir, "9.0.unf"))
	if err != nil || string(data) != "manual backup 9.0.unf" {
		t.Errorf("file: %q %v", data, err)
	}
	if info, _ := os.Stat(filepath.Join(dir, "9.0.unf")); info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", info.Mode())
	}
	if r := env.mock.Last(); r.Path != "/proxy/network/dl/backup/9.0.unf" || r.Header.Get("X-API-KEY") != "key123" {
		t.Errorf("request %s %v", r.Path, r.Header)
	}

	env.call("unifi_download_backup", `{"filename":"autobackup_9.0_1.unf"}`)
	eq(t, "auto", env.mock.Last().Path, "/proxy/network/dl/autobackup/autobackup_9.0_1.unf")

	// An existing file is kept unless overwrite is true.
	contains(t, "exists", env.callErr("unifi_download_backup", `{"source":"/dl/backup/9.0.unf"}`), "already exists; pass overwrite: true")
	env.call("unifi_download_backup", `{"source":"/dl/backup/9.0.unf","overwrite":true}`)

	// Failed and empty downloads leave nothing behind.
	contains(t, "404", env.callErr("unifi_download_backup", `{"filename":"missing.unf"}`), "HTTP 404")
	contains(t, "empty", env.callErr("unifi_download_backup", `{"filename":"empty.unf"}`), "empty file")
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	eq(t, "files", strings.Join(names, ","), "9.0.unf,autobackup_9.0_1.unf")

	// A dry run downloads and writes nothing.
	before := len(env.mock.Requests())
	dry := env.call("unifi_download_backup", `{"source":"/dl/backup/other.unf","dry_run":true}`)
	eq(t, "dry", get(dry, "result", "would_save"), filepath.Join(dir, "other.unf"))
	if len(env.mock.Requests()) != before {
		t.Error("dry run sent a request")
	}
	if _, err := os.Stat(filepath.Join(dir, "other.unf")); err == nil {
		t.Error("dry run wrote a file")
	}
}

// Names and paths that could reach outside UNIFI_BACKUP_DIR, or anything that
// isn't a backup, are refused before any request.
func TestDownloadBackupRefusesOtherPaths(t *testing.T) {
	env, dir := backupEnv(t)
	before := len(env.mock.Requests())
	for _, args := range []string{
		`{"filename":"../escape.unf"}`,
		`{"filename":"a/b.unf"}`,
		`{"filename":".hidden.unf"}`,
		`{"filename":"backup.txt"}`,
		`{"source":"/dl/backup/../../etc/passwd"}`,
		`{"source":"/api/s/default/stat/device"}`,
		`{"source":"/dl/backup/x.unf/extra"}`,
		`{"source":"/dl/other/x.unf"}`,
	} {
		contains(t, args, env.callErr("unifi_download_backup", args), "Not a backup download")
	}
	contains(t, "both", env.callErr("unifi_download_backup", `{"source":"/dl/backup/a.unf","filename":"a.unf"}`), "not both")
	contains(t, "neither", env.callErr("unifi_download_backup", `{}`), "Pass source")
	if len(env.mock.Requests()) != before {
		t.Error("refused calls sent requests")
	}
	// A symlink named like the backup isn't followed out of the directory.
	outside := filepath.Join(t.TempDir(), "target.unf")
	if err := os.Symlink(outside, filepath.Join(dir, "link.unf")); err != nil {
		t.Skip(err)
	}
	env.call("unifi_download_backup", `{"source":"/dl/backup/link.unf","overwrite":true}`)
	if _, err := os.Stat(outside); err == nil {
		t.Error("wrote through a symlink outside the backup directory")
	}
	if info, err := os.Lstat(filepath.Join(dir, "link.unf")); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Errorf("link.unf should now be a regular file: %v %v", info, err)
	}
}

func TestDownloadBackupNeedsBackupDir(t *testing.T) {
	opts := toolOptions(true, false)
	opts.BackupDir = ""
	env := startMock(t, opts)
	if toolMap(t, env.mcp)["unifi_download_backup"] != nil {
		t.Error("registered without UNIFI_BACKUP_DIR")
	}
	if toolMap(t, connect(t, env.client, toolOptions(false, false)))["unifi_download_backup"] != nil {
		t.Error("registered without writes")
	}
}
