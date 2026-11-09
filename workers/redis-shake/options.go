package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

const (
	maxExcludedPrefixes = 64
	maxPrefixLength     = 256
)

// Options are the migration options of POST /start.
type Options struct {
	// FlushTarget allows a target that already holds keys: they are deleted
	// before the copy. Without it, the target must be empty.
	FlushTarget bool `json:"flushTarget"`
	// ExcludeKeyPrefixes are neither copied nor replicated.
	ExcludeKeyPrefixes []string `json:"excludeKeyPrefixes"`
}

func decodeOptions(raw json.RawMessage) (Options, error) {
	var options Options
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return options, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&options); err != nil {
		return Options{}, errors.New("options must be a JSON object with flushTarget and excludeKeyPrefixes")
	}
	if decoder.More() {
		return Options{}, errors.New("options must be a single JSON object")
	}
	return options, options.validate()
}

func (o Options) validate() error {
	if len(o.ExcludeKeyPrefixes) > maxExcludedPrefixes {
		return fmt.Errorf("at most %d key prefixes can be excluded", maxExcludedPrefixes)
	}
	for _, prefix := range o.ExcludeKeyPrefixes {
		switch {
		case prefix == "" || len(prefix) > maxPrefixLength:
			return fmt.Errorf("excluded key prefixes must be 1-%d bytes long", maxPrefixLength)
		case strings.IndexFunc(prefix, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0:
			return fmt.Errorf("excluded key prefix %q contains unprintable characters", prefix)
		case strings.HasPrefix(keyPrefix, prefix):
			return fmt.Errorf("excluded key prefix %q would exclude the migration's own keys (%s…)", prefix, keyPrefix)
		}
	}
	return nil
}
