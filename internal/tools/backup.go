package tools

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
)

// backupName is a backup file name as the controller writes them. Anything
// else, such as a name with a path separator, is refused before a request.
var backupName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.unf$`)

// backupPath is where the Network application serves backups: manual ones
// (cmd/backup's url) under /dl/backup/, automatic ones under /dl/autobackup/.
var backupPath = regexp.MustCompile(`^/dl/(?:backup|autobackup)/([^/]+)$`)

// downloadBackup saves a backup in dir. It writes through an os.Root, so
// nothing outside dir can be created or replaced, whatever the name.
func downloadBackup(ctx context.Context, client *unifi.Client, dir string, a Args) (any, error) {
	path, filename := a.Str("path"), a.Str("filename")
	switch {
	case path != "" && filename != "":
		return nil, errorf("Pass either path or filename, not both")
	case filename != "":
		path = "/dl/autobackup/" + filename
	case path == "":
		return nil, errorf("Pass path (download_path from unifi_create_backup) or filename (from unifi_list_backups)")
	}
	m := backupPath.FindStringSubmatch(path)
	if m == nil || !backupName.MatchString(m[1]) {
		return nil, fmt.Errorf("Not a backup download: %q. Expected /dl/backup/<name>.unf or /dl/autobackup/<name>.unf, or a filename ending in .unf", path)
	}
	name := m[1]
	dest := filepath.Join(dir, name)
	overwrite := a.Truthy("overwrite")

	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("UNIFI_BACKUP_DIR: %s", err.Error())
	}
	defer root.Close()
	if _, err := root.Lstat(name); err == nil && !overwrite {
		return nil, fmt.Errorf("%s already exists; pass overwrite: true to replace it", dest)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("UNIFI_BACKUP_DIR: %s", err.Error())
	}

	if call := unifi.CallFrom(ctx); call != nil && call.DryRun() {
		return ojson.New("would_download", path, "would_save", dest, "overwrite", overwrite), nil
	}

	// Download to a temporary name and rename it into place, so a failed or
	// partial download never leaves a truncated backup under the real name.
	tmp := "." + name + "." + rand.Text() + ".part"
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("UNIFI_BACKUP_DIR: cannot write there: %s", err.Error())
	}
	n, err := client.Request(ctx, "GET", path, &unifi.Options{Body: U, Sink: f})
	if cerr := f.Close(); err == nil && cerr != nil {
		err = fmt.Errorf("could not save the download: %s", cerr.Error())
	}
	if err == nil && ojson.ToNumber(n) == 0 {
		err = fmt.Errorf("GET %s returned an empty file", path)
	}
	if err != nil {
		_ = root.Remove(tmp)
		return nil, err
	}
	if err := root.Rename(tmp, name); err != nil {
		_ = root.Remove(tmp)
		return nil, fmt.Errorf("could not save the download as %s: %s", dest, err.Error())
	}
	return ojson.New("saved", dest, "bytes", n, "source", path), nil
}
