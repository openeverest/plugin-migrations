package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Catalog reads and writes pgcopydb's SQLite catalog. The pgcopydb CLI is
// avoided on purpose: in v0.18 "list progress" rewrites the stored filters of a
// running filtered clone, "sentinel get --json" misreports endpos, and
// "sentinel set" refuses catalogs created with --filters.
type Catalog struct {
	path string
}

func NewCatalog(workDir string) *Catalog {
	return &Catalog{path: filepath.Join(workDir, "schema", "source.db")}
}

var errCatalogMissing = errors.New("pgcopydb catalog does not exist yet")

// Sentinel mirrors pgcopydb's sentinel row. Missing LSNs are empty strings.
type Sentinel struct {
	StartPos  string `json:"startpos"`
	EndPos    string `json:"endpos"`
	Apply     bool   `json:"-"`
	WriteLSN  string `json:"write_lsn"`
	ReplayLSN string `json:"replay_lsn"`
}

// Progress summarizes the base copy recorded in the catalog.
type Progress struct {
	CopyDone    bool
	BytesTotal  uint64
	BytesCopied uint64
	Sentinel    *Sentinel
}

const (
	totalWallClockLabel = "Total Wall Clock Duration"
	sqliteBusyTimeoutMS = 5000
)

func (c *Catalog) Read(ctx context.Context) (Progress, error) {
	var progress Progress
	var rows []struct {
		CopyDone    int64  `json:"copy_done"`
		BytesTotal  uint64 `json:"bytes_total"`
		BytesCopied uint64 `json:"bytes_copied"`
	}
	// Both totals use on-disk table sizes, so the copy reaches exactly 100%.
	// summary rows with a table and no index or constraint are COPY jobs.
	query := fmt.Sprintf(`select
  (select count(*) from timings where label = '%s' and done_time_epoch is not null) as copy_done,
  (select coalesce(sum(bytes), 0) from s_table_size) as bytes_total,
  (select coalesce(sum(bytes), 0) from s_table_size where oid in (
     select tableoid from summary
      where tableoid is not null and indexoid is null and conoid is null and done_time_epoch is not null)) as bytes_copied`,
		totalWallClockLabel)
	if err := c.query(ctx, query, &rows); err != nil {
		return progress, err
	}
	if len(rows) == 1 {
		progress.CopyDone = rows[0].CopyDone > 0
		progress.BytesTotal = rows[0].BytesTotal
		progress.BytesCopied = rows[0].BytesCopied
	}
	if progress.CopyDone {
		progress.BytesCopied = progress.BytesTotal
	}

	var sentinels []struct {
		Sentinel
		ApplyFlag int `json:"apply_flag"`
	}
	err := c.query(ctx, "select coalesce(startpos, '') as startpos, coalesce(endpos, '') as endpos, coalesce(apply, 0) as apply_flag, coalesce(write_lsn, '') as write_lsn, coalesce(replay_lsn, '') as replay_lsn from sentinel", &sentinels)
	if err != nil {
		return progress, err
	}
	if len(sentinels) == 1 {
		sentinel := sentinels[0].Sentinel
		sentinel.Apply = sentinels[0].ApplyFlag == 1
		progress.Sentinel = &sentinel
	}
	return progress, nil
}

func (c *Catalog) SetApply(ctx context.Context, apply bool) error {
	value := 0
	if apply {
		value = 1
	}
	return c.exec(ctx, fmt.Sprintf("update sentinel set apply = %d", value))
}

func (c *Catalog) SetEndPos(ctx context.Context, lsn string) error {
	if _, err := parseLSN(lsn); err != nil {
		return err
	}
	return c.exec(ctx, fmt.Sprintf("update sentinel set endpos = '%s'", lsn))
}

func (c *Catalog) query(ctx context.Context, sql string, destination any) error {
	output, err := c.sqlite(ctx, true, sql)
	if err != nil {
		return err
	}
	if strings.TrimSpace(output) == "" {
		return nil
	}
	return json.Unmarshal([]byte(output), destination)
}

func (c *Catalog) exec(ctx context.Context, sql string) error {
	_, err := c.sqlite(ctx, false, sql)
	return err
}

func (c *Catalog) sqlite(ctx context.Context, readOnly bool, sql string) (string, error) {
	if _, err := os.Stat(c.path); err != nil {
		return "", errCatalogMissing
	}
	args := []string{"-cmd", fmt.Sprintf(".timeout %d", sqliteBusyTimeoutMS)}
	if readOnly {
		args = append(args, "-readonly", "-json")
	}
	args = append(args, c.path, sql)
	command := exec.CommandContext(ctx, "sqlite3", args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("sqlite3: %s", firstLine(stderr.String(), err.Error()))
	}
	return stdout.String(), nil
}
