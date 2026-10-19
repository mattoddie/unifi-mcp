package unifi

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	badPathChars  = regexp.MustCompile("[\\\\\x00-\x1f\x7f]")
	queryChars    = regexp.MustCompile(`[?#]`)
	encodedDot    = regexp.MustCompile(`(?i)%2e`)
	percentEscape = regexp.MustCompile(`%[0-9A-Fa-f]{2}`)
	// Escapes of letters, digits, "-", "_" and "~" (unreserved characters other than
	// the dot, which has its own check). They never need encoding and decode to the
	// same path, so the only reason to send one is to hide a word from a check, as in
	// batch-d%65lete.
	encodedUnreserved = regexp.MustCompile(`(?i)%(?:3[0-9]|4[1-9a-f]|5[0-9a]|6[1-9a-f]|7[0-9a]|2d|5f|7e)`)
)

// AssertSafePath rejects request paths that could be normalised to somewhere
// else once turned into a URL: dot segments (also percent-encoded, e.g.
// %2e%2e), backslashes, control characters and embedded query strings or
// fragments. Without this, /%2e%2e/api/system would resolve outside the
// Network application. Percent-encoded letters, digits, "-", "_" and "~" are
// rejected too: they never need encoding, so the path a check sees is the path
// the controller acts on.
func AssertSafePath(path string) error {
	if !strings.HasPrefix(path, "/") {
		return &Error{Message: fmt.Sprintf(`Invalid API path "%s": it must start with /`, path)}
	}
	decoded, ok := decodeURIComponent(path)
	if !ok {
		return &Error{Message: fmt.Sprintf(`Invalid API path "%s": malformed percent-encoding`, path)}
	}
	if m := encodedUnreserved.FindString(path); m != "" {
		return &Error{Message: fmt.Sprintf(`Invalid API path "%s": %s encodes a character that doesn't need encoding; write it unencoded`, path, m)}
	}
	for _, p := range []string{path, decoded} {
		if badPathChars.MatchString(p) {
			return &Error{Message: fmt.Sprintf(`Invalid API path "%s": backslashes and control characters are not allowed`, path)}
		}
		if queryChars.MatchString(p) {
			return &Error{Message: fmt.Sprintf(`Invalid API path "%s": pass query parameters separately, not in the path`, path)}
		}
		if encodedDot.MatchString(p) {
			return &Error{Message: fmt.Sprintf(`Invalid API path "%s": encoded dots (%%2e) are not allowed`, path)}
		}
		for _, seg := range strings.Split(p, "/") {
			if seg == "." || seg == ".." {
				return &Error{Message: fmt.Sprintf(`Invalid API path "%s": "." and ".." segments are not allowed`, path)}
			}
		}
	}
	return nil
}

// decodeURIComponent decodes %XX escapes, failing on malformed escapes or
// escapes that don't form valid UTF-8, like JavaScript's decodeURIComponent.
func decodeURIComponent(s string) (string, bool) {
	if !strings.Contains(s, "%") {
		return s, true
	}
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && (i+2 >= len(s) || !percentEscape.MatchString(s[i:i+3])) {
			return "", false
		}
	}
	out, err := url.PathUnescape(s)
	if err != nil || !validUTF8(out) {
		return "", false
	}
	return out, true
}

func validUTF8(s string) bool { return utf8.ValidString(s) }

// PathSegment encodes one path segment (site name, object ID, ...), refusing
// values that would be read as a relative path.
func PathSegment(value, what string) (string, error) {
	if what == "" {
		what = "path segment"
	}
	if value == "" || value == "." || value == ".." {
		return "", &Error{Message: fmt.Sprintf(`Invalid %s "%s"`, what, value)}
	}
	return EncodeURIComponent(value), nil
}

// EncodeURIComponent is JavaScript's encodeURIComponent.
func EncodeURIComponent(s string) string {
	const unreserved = "-_.!~*'()"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(unreserved, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// whatwgPathname is the pathname the WHATWG URL parser (Node's `new URL(raw).pathname`)
// gives for raw, restricted to what can reach it after AssertSafePath: the
// characters it percent-encodes in a path, and dot segments.
func whatwgPathname(raw string) string {
	rest := raw
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
		if j := strings.IndexAny(rest, "/?#"); j >= 0 {
			rest = rest[j:]
		} else {
			rest = ""
		}
	}
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest = rest[:i]
	}
	rest = strings.ReplaceAll(rest, "\\", "/")
	if rest == "" {
		return "/"
	}
	var b strings.Builder
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		switch {
		case c <= 0x20 || c >= 0x7f || strings.IndexByte("\"<>^`{}", c) >= 0:
			fmt.Fprintf(&b, "%%%02X", c)
		default:
			b.WriteByte(c)
		}
	}
	segs := strings.Split(b.String(), "/")
	var out []string
	for i, seg := range segs {
		if i == 0 {
			continue
		}
		last := i == len(segs)-1
		switch strings.ToLower(seg) {
		case "..", ".%2e", "%2e.", "%2e%2e":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
			if last {
				out = append(out, "")
			}
		case ".", "%2e":
			if last {
				out = append(out, "")
			}
		default:
			out = append(out, seg)
		}
	}
	return "/" + strings.Join(out, "/")
}

// formEncode encodes a query parameter like URLSearchParams (application/x-www-form-urlencoded).
func formEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '*' || c == '-' || c == '.' || c == '_':
			b.WriteByte(c)
		case c == ' ':
			b.WriteByte('+')
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
