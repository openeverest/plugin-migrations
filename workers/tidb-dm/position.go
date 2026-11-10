package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// sourcePosition is where the source's binary log was when it was read.
type sourcePosition struct {
	File string
	Pos  uint64
	GTID string
}

// reached tells whether a DM checkpoint ("(file, pos)" and a GTID set) is at
// or past p: with GTIDs, the checkpoint contains every transaction in p's
// set; otherwise its file/position is not before p's.
func (p sourcePosition) reached(checkpoint, checkpointGTID string, useGTID bool) (bool, error) {
	if useGTID {
		want, err := parseGTIDSet(p.GTID)
		if err != nil {
			return false, err
		}
		have, err := parseGTIDSet(checkpointGTID)
		if err != nil {
			return false, err
		}
		return have.contains(want), nil
	}
	file, pos, err := parseDMPosition(checkpoint)
	if err != nil {
		return false, err
	}
	switch compareBinlogFiles(file, p.File) {
	case 1:
		return true, nil
	case 0:
		return pos >= p.Pos, nil
	}
	return false, nil
}

var dmPositionPattern = regexp.MustCompile(`^\(\s*([^,\s]+)\s*,\s*(\d+)\s*\)$`)

// parseDMPosition reads DM's "(binlog.000003, 111652)".
func parseDMPosition(position string) (string, uint64, error) {
	match := dmPositionPattern.FindStringSubmatch(strings.TrimSpace(position))
	if match == nil {
		return "", 0, fmt.Errorf("unexpected binlog position %q", truncate(position, 80))
	}
	pos, err := strconv.ParseUint(match[2], 10, 64)
	return match[1], pos, err
}

// compareBinlogFiles orders "binlog.000009" before "binlog.000010". DM may
// append a relay suffix ("binlog|000001.000003"), which is ignored.
func compareBinlogFiles(a, b string) int {
	baseA, numberA := splitBinlogFile(a)
	baseB, numberB := splitBinlogFile(b)
	if baseA != baseB {
		return strings.Compare(a, b)
	}
	switch {
	case numberA < numberB:
		return -1
	case numberA > numberB:
		return 1
	}
	return 0
}

func splitBinlogFile(name string) (string, uint64) {
	if base, _, found := strings.Cut(name, "|"); found {
		name = base + name[strings.LastIndex(name, "."):]
	}
	dot := strings.LastIndex(name, ".")
	if dot < 0 {
		return name, 0
	}
	number, err := strconv.ParseUint(name[dot+1:], 10, 64)
	if err != nil {
		return name, 0
	}
	return name[:dot], number
}

type interval struct{ start, end uint64 }

// gtidSet maps a source UUID (with its tag, on MySQL 8.4+) to the
// transaction numbers it contains.
type gtidSet map[string][]interval

// parseGTIDSet reads MySQL's "uuid:1-5:7,uuid2:1-3" (tagged:
// "uuid:tag:1-5").
func parseGTIDSet(text string) (gtidSet, error) {
	set := gtidSet{}
	text = strings.Join(strings.Fields(text), "")
	if text == "" {
		return set, nil
	}
	for _, entry := range strings.Split(text, ",") {
		parts := strings.Split(entry, ":")
		if len(parts) < 2 || parts[0] == "" {
			return nil, fmt.Errorf("unexpected GTID set entry %q", truncate(entry, 80))
		}
		key := strings.ToLower(parts[0])
		for _, part := range parts[1:] {
			first, last, isRange := strings.Cut(part, "-")
			start, err := strconv.ParseUint(first, 10, 64)
			if err != nil {
				// A tag names the transactions that follow it.
				key = strings.ToLower(parts[0]) + ":" + strings.ToLower(part)
				continue
			}
			end := start
			if isRange {
				if end, err = strconv.ParseUint(last, 10, 64); err != nil || end < start {
					return nil, fmt.Errorf("unexpected GTID interval %q", truncate(part, 40))
				}
			}
			set[key] = append(set[key], interval{start, end})
		}
	}
	return set, nil
}

// contains tells whether every transaction of other is in s.
func (s gtidSet) contains(other gtidSet) bool {
	for key, wanted := range other {
		for _, want := range wanted {
			if !covered(s[key], want) {
				return false
			}
		}
	}
	return true
}

// covered tells whether the intervals cover want; they may overlap or touch.
func covered(intervals []interval, want interval) bool {
	next := want.start
	for progress := true; progress && next <= want.end; {
		progress = false
		for _, have := range intervals {
			if have.start <= next && have.end >= next {
				next = have.end + 1
				progress = true
			}
		}
	}
	return next > want.end
}
