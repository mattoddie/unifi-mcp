package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/mattoddie/unifi-mcp/internal/ojson"
)

func registryCmd(args []string) error {
	fs := flag.NewFlagSet("registry", flag.ExitOnError)
	version := fs.String("version", "", "the release's version (X.Y.Z, without the v)")
	file := fs.String("f", "server.json", "the committed server.json")
	bundles := fs.String("bundles", "dist", "directory with the release's .mcpb files")
	out := fs.String("o", "dist/server.json", "the server.json to write")
	_ = fs.Parse(args)
	if *version == "" {
		return errors.New("registry needs -version\n\n" + usage)
	}
	data, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	text, err := stampServerJSON(data, *version, *bundles)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(*out, []byte(text), 0o644); err != nil {
		return err
	}
	fmt.Printf("Wrote %s for %s\n", *out, *version)
	return nil
}

// bundleName matches the platform part of a bundle's file name,
// e.g. "darwin-arm64" in unifi-mcp-1.2.3-darwin-arm64.mcpb.
var bundleName = regexp.MustCompile(`^unifi-mcp-.+-([a-z0-9]+-[a-z0-9]+)\.mcpb$`)

// stampServerJSON returns the committed server.json (which carries a
// placeholder version) for a release: the version, the image tag, and each
// bundle's download URL and SHA-256, hashed from the files in bundles. Every
// bundle in the directory must be listed, so a new platform can't be left out.
func stampServerJSON(data []byte, version, bundles string) (string, error) {
	if !semver.MatchString(version) {
		return "", fmt.Errorf("version %q is not a semantic version like 1.2.3", version)
	}
	v, err := ojson.Parse(data)
	if err != nil {
		return "", fmt.Errorf("server.json: %w", err)
	}
	s, ok := ojson.AsObj(v)
	if !ok {
		return "", errors.New("server.json: not a JSON object")
	}
	s.Set("version", version)

	files, err := filepath.Glob(filepath.Join(bundles, "*.mcpb"))
	if err != nil {
		return "", err
	}
	unlisted := map[string]bool{}
	for _, f := range files {
		unlisted[filepath.Base(f)] = true
	}
	for _, p := range arr(s.Get("packages")) {
		pkg, _ := ojson.AsObj(p)
		id := ojson.String(pkg.Get("identifier"))
		switch ojson.String(pkg.Get("registryType")) {
		case "oci":
			i := strings.LastIndex(id, ":")
			if i < 0 || strings.Contains(id[i:], "/") {
				return "", fmt.Errorf("OCI identifier %q has no tag", id)
			}
			pkg.Set("identifier", id[:i+1]+version)
		case "mcpb":
			i := strings.Index(id, "/releases/download/")
			m := bundleName.FindStringSubmatch(id[strings.LastIndex(id, "/")+1:])
			if i < 0 || m == nil {
				return "", fmt.Errorf("MCPB identifier %q isn't a release download of a unifi-mcp bundle", id)
			}
			name := "unifi-mcp-" + version + "-" + m[1] + ".mcpb"
			sum, err := sha256File(filepath.Join(bundles, name))
			if err != nil {
				return "", err
			}
			delete(unlisted, name)
			pkg.Set("identifier", id[:i]+"/releases/download/v"+version+"/"+name)
			pkg.Set("fileSha256", sum)
		}
	}
	if len(unlisted) > 0 {
		names := make([]string, 0, len(unlisted))
		for n := range unlisted {
			names = append(names, n)
		}
		sort.Strings(names)
		return "", fmt.Errorf("server.json doesn't list %s: add a package for each", strings.Join(names, ", "))
	}
	return ojson.StringifyIndent(s) + "\n", nil
}

func sha256File(name string) (string, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
