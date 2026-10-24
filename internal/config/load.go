package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/tailscale/hujson"
)

// load decodes the JSONC file at path into out, which holds defaults.
// sig describes exactly the bytes that were read. Errors give position and
// kind only, never file content: the file holds api_key.
func load(path string, out any) (fileSig, error) {
	raw, sig, err := readFile(path)
	if err != nil {
		return sig, err
	}
	std, err := hujson.Standardize(raw)
	if err != nil {
		return sig, fmt.Errorf("invalid JSONC: %s", syntaxError(err))
	}
	dec := json.NewDecoder(bytes.NewReader(std))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return sig, decodeError(err, std)
	}
	return sig, nil
}

// readFile reads path and stats the same open file, so the signature always
// matches the content even if the file is replaced meanwhile.
func readFile(path string) ([]byte, fileSig, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fileSig{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, fileSig{}, err
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return nil, fileSig{}, err
	}
	return raw, fileSig{mtime: st.ModTime(), size: st.Size(), ok: true}, nil
}

var hujsonPosRe = regexp.MustCompile(`line (\d+), column (\d+): (.*)`)

// syntaxError keeps the position of a hujson error and drops the literal it quotes.
func syntaxError(err error) string {
	m := hujsonPosRe.FindStringSubmatch(err.Error())
	if m == nil {
		return "syntax error"
	}
	kind := "syntax error"
	switch msg := m[3]; {
	case strings.HasPrefix(msg, "invalid literal"):
		kind = "invalid literal"
	case strings.HasPrefix(msg, "invalid character"):
		kind = "invalid character"
	case strings.Contains(msg, "unexpected EOF"):
		kind = "unexpected end of file"
	case strings.HasPrefix(msg, "invalid UTF-8"):
		kind = "invalid UTF-8"
	}
	return fmt.Sprintf("line %s, column %s: %s", m[1], m[2], kind)
}

// decodeError describes a decode error of the standardized JSON std
// (same offsets as the file) without quoting its content.
func decodeError(err error, std []byte) error {
	var typeErr *json.UnmarshalTypeError
	var syntaxErr *json.SyntaxError
	switch {
	case errors.As(err, &typeErr):
		return fmt.Errorf("%s: %s has the wrong type (want %s)", position(std, typeErr.Offset), typeErr.Field, typeErr.Type)
	case errors.As(err, &syntaxErr):
		return fmt.Errorf("%s: syntax error", position(std, syntaxErr.Offset))
	}
	if name, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		name, _ = strconv.Unquote(name)
		return fmt.Errorf("%s: unknown field", memberPosition(std, func(n string, _ *hujson.Value) bool { return n == name }))
	}
	// Otherwise a CIDR field failed netip parsing.
	isBadCIDR := func(n string, v *hujson.Value) bool {
		lit, ok := v.Value.(hujson.Literal)
		if !strings.HasSuffix(n, "_cidr") || n == "lan_cidr" || !ok {
			return false
		}
		_, perr := netip.ParsePrefix(lit.String())
		return perr != nil
	}
	return fmt.Errorf("%s: invalid CIDR", memberPosition(std, isBadCIDR))
}

// memberPosition finds the first object member, at any depth, that matches.
func memberPosition(std []byte, match func(name string, v *hujson.Value) bool) string {
	root, err := hujson.Parse(std)
	if err != nil {
		return "unknown position"
	}
	var find func(v *hujson.Value) (int, bool)
	find = func(v *hujson.Value) (int, bool) {
		obj, ok := v.Value.(*hujson.Object)
		if !ok {
			return 0, false
		}
		for i := range obj.Members {
			m := &obj.Members[i]
			name, _ := m.Name.Value.(hujson.Literal)
			if match(name.String(), &m.Value) {
				return m.Name.StartOffset, true
			}
			if off, ok := find(&m.Value); ok {
				return off, true
			}
		}
		return 0, false
	}
	off, ok := find(&root)
	if !ok {
		return "unknown position"
	}
	return position(std, int64(off))
}

func position(b []byte, offset int64) string {
	offset = min(max(offset, 0), int64(len(b)))
	before := b[:offset]
	line := bytes.Count(before, []byte("\n")) + 1
	column := len(before) - bytes.LastIndexByte(before, '\n')
	return fmt.Sprintf("line %d, column %d", line, column)
}
