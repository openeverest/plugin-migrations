package redisshake

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode"

	"github.com/openeverest/plugin-migrations/backend/internal/engine"
)

const (
	maxExcludedPrefixes = 64
	maxPrefixLength     = 256
	// ownKeyPrefix starts the keys the worker writes on both sides.
	ownKeyPrefix = "__openeverest_migration:"
)

// Options is the public, credential-free shape stored in the migration
// record. The worker accepts the same fields.
type Options struct {
	// FlushTarget deletes every key on the target before the copy. Without
	// it, the target must be empty.
	FlushTarget bool `json:"flushTarget"`
	// ExcludeKeyPrefixes are neither copied nor replicated.
	ExcludeKeyPrefixes []string `json:"excludeKeyPrefixes"`
}

func invalidOptions(format string, args ...any) error {
	return &engine.Error{Kind: engine.KindInvalidRequest, Message: fmt.Sprintf(format, args...)}
}

func decodeOptions(raw json.RawMessage) (Options, error) {
	options := Options{ExcludeKeyPrefixes: []string{}}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) != 0 && !bytes.Equal(trimmed, []byte("null")) {
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&options); err != nil {
			return Options{}, invalidOptions("invalid options: use flushTarget (boolean) and excludeKeyPrefixes (list of strings)")
		}
		if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
			return Options{}, invalidOptions("invalid options: unexpected data after JSON object")
		}
	}
	if options.ExcludeKeyPrefixes == nil {
		options.ExcludeKeyPrefixes = []string{}
	}
	if len(options.ExcludeKeyPrefixes) > maxExcludedPrefixes {
		return Options{}, invalidOptions("excludeKeyPrefixes accepts at most %d prefixes", maxExcludedPrefixes)
	}
	prefixes := make([]string, 0, len(options.ExcludeKeyPrefixes))
	for _, prefix := range options.ExcludeKeyPrefixes {
		switch {
		case prefix == "" || len(prefix) > maxPrefixLength:
			return Options{}, invalidOptions("excluded key prefixes must be 1-%d bytes long", maxPrefixLength)
		case strings.IndexFunc(prefix, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0:
			return Options{}, invalidOptions("excluded key prefix %q contains unprintable characters", prefix)
		case strings.HasPrefix(ownKeyPrefix, prefix):
			return Options{}, invalidOptions("excluded key prefix %q would exclude the migration's own keys (%s…)", prefix, ownKeyPrefix)
		case slices.Contains(prefixes, prefix):
			continue
		}
		prefixes = append(prefixes, prefix)
	}
	options.ExcludeKeyPrefixes = prefixes
	return options, nil
}
