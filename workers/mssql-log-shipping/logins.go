package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"slices"
	"strings"
)

// sqlLogin is a SQL Server login as sys.sql_logins describes it. The
// password hash is a credential: never log it.
type sqlLogin struct {
	Name            string
	SID             []byte
	PasswordHash    []byte
	DefaultDatabase string
	CheckPolicy     bool
	CheckExpiration bool
	Disabled        bool
}

// copyLogins creates on the target the SQL logins that the migrated
// databases' users map to, with the same SID (so the users stay mapped) and
// password hash. Logins are server objects: on an availability group they
// are created on every replica. It returns notes for what needs a hand.
func (a *Agent) copyLogins(ctx context.Context, databases []string) ([]string, error) {
	replica, err := a.replicaDB()
	if err != nil {
		return nil, err
	}
	users, windows, err := mappedUsers(ctx, replica, databases)
	if err != nil {
		return nil, fmt.Errorf("list the users of the migrated databases: %s", sqlError(err))
	}
	var notes []string
	if len(windows) > 0 {
		notes = append(notes, "Windows logins are not copied: "+strings.Join(windows, ", ")+".")
	}
	source, err := a.source()
	if err != nil {
		return nil, err
	}
	sids := make([][]byte, len(users))
	for i, user := range users {
		sids[i] = user.SID
	}
	logins, err := readLogins(ctx, source, sids)
	if err != nil {
		return nil, fmt.Errorf("read the logins on the source: %s", sqlError(err))
	}
	if orphans := orphanedUsers(users, logins); len(orphans) > 0 {
		notes = append(notes, "These users have no SQL login on the source server, so none was created: "+strings.Join(orphans, ", ")+".")
	}
	hosts := []string{""}
	if others, err := replicaHosts(ctx, replica); err == nil {
		a.mu.Lock()
		pinned := a.replica.Host
		a.mu.Unlock()
		for _, host := range others {
			if !strings.EqualFold(host, pinned) {
				hosts = append(hosts, host)
			}
		}
	}
	for _, host := range hosts {
		db := replica
		where := "the target"
		if host != "" {
			if db, err = a.db(a.config.Target.WithHost(host)); err != nil {
				return nil, err
			}
			where = "replica " + host
		}
		for _, login := range logins {
			note, err := createLogin(ctx, db, login)
			if err != nil {
				log.Printf("copy login %s to %s: %s", login.Name, where, sqlError(err))
				notes = append(notes, fmt.Sprintf("Login %s could not be created on %s: %s.", login.Name, where, sqlError(err)))
				continue
			}
			if note != "" {
				notes = append(notes, note)
			}
		}
	}
	return notes, nil
}

// mappedUser is a login SID and the database users (or owners) mapped to it.
type mappedUser struct {
	SID   []byte
	Users []string
	// Owner is set when the SID owns a database; owners are often sa.
	Owner bool
}

// mappedUsers lists the logins the databases' SQL users and owners map to,
// read on the target after recovery (the source may be restoring).
func mappedUsers(ctx context.Context, db *sql.DB, databases []string) ([]mappedUser, []string, error) {
	var users []mappedUser
	var windows []string
	add := func(sid []byte, user string, owner bool) {
		if len(sid) == 0 {
			return
		}
		index := slices.IndexFunc(users, func(existing mappedUser) bool { return slices.Equal(existing.SID, sid) })
		if index < 0 {
			users = append(users, mappedUser{SID: sid})
			index = len(users) - 1
		}
		if owner {
			users[index].Owner = true
		} else {
			users[index].Users = append(users[index].Users, user)
		}
	}
	for _, database := range databases {
		rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT name, sid, type FROM %s.sys.database_principals
			WHERE type IN ('S', 'U', 'G') AND sid IS NOT NULL AND authentication_type IN (1, 3) AND principal_id NOT IN (1, 2, 3, 4)`,
			quoteName(database)))
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			var name, kind string
			var sid []byte
			if err := rows.Scan(&name, &sid, &kind); err != nil {
				rows.Close()
				return nil, nil, err
			}
			if kind == "S" {
				add(sid, database+"."+name, false)
			} else if !slices.Contains(windows, name) {
				windows = append(windows, name)
			}
		}
		if err := rows.Close(); err != nil {
			return nil, nil, err
		}
		var owner []byte
		if err := db.QueryRowContext(ctx, `SELECT owner_sid FROM sys.databases WHERE name = @p1`, database).Scan(&owner); err != nil {
			return nil, nil, err
		}
		add(owner, "", true)
	}
	return users, windows, nil
}

// orphanedUsers lists the users whose login the source does not have, so
// their login cannot be copied (owners are often sa or a Windows login).
func orphanedUsers(users []mappedUser, logins []sqlLogin) []string {
	var orphans []string
	for _, user := range users {
		if len(user.Users) == 0 || slices.ContainsFunc(logins, func(login sqlLogin) bool { return slices.Equal(login.SID, user.SID) }) {
			continue
		}
		orphans = append(orphans, user.Users...)
	}
	return orphans
}

func readLogins(ctx context.Context, db *sql.DB, sids [][]byte) ([]sqlLogin, error) {
	var logins []sqlLogin
	for _, sid := range sids {
		var login sqlLogin
		err := db.QueryRowContext(ctx, `SELECT name, sid, password_hash, ISNULL(default_database_name, N'master'),
			is_policy_checked, is_expiration_checked, is_disabled FROM sys.sql_logins WHERE sid = @p1`, sid).
			Scan(&login.Name, &login.SID, &login.PasswordHash, &login.DefaultDatabase, &login.CheckPolicy, &login.CheckExpiration, &login.Disabled)
		if err == sql.ErrNoRows {
			continue // a user without a login, or a login of another kind
		}
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(login.Name, "sa") || strings.HasPrefix(login.Name, "##") || len(login.PasswordHash) == 0 {
			continue
		}
		logins = append(logins, login)
	}
	return logins, nil
}

// createLogin creates the login unless one with its name exists. A login
// with the same name and another SID is reported, not changed.
func createLogin(ctx context.Context, db *sql.DB, login sqlLogin) (string, error) {
	var existing []byte
	err := db.QueryRowContext(ctx, `SELECT sid FROM sys.server_principals WHERE name = @p1`, login.Name).Scan(&existing)
	switch {
	case err == nil && slices.Equal(existing, login.SID):
		return "", nil
	case err == nil:
		return fmt.Sprintf("Login %s exists on the target with another SID; map its users with ALTER USER … WITH LOGIN = %s.",
			login.Name, quoteName(login.Name)), nil
	case err != sql.ErrNoRows:
		return "", err
	}
	var defaultExists int
	if err := db.QueryRowContext(ctx, `SELECT CASE WHEN DB_ID(@p1) IS NULL THEN 0 ELSE 1 END`, login.DefaultDatabase).Scan(&defaultExists); err != nil {
		return "", err
	}
	defaultDatabase := login.DefaultDatabase
	if defaultExists == 0 {
		defaultDatabase = "master"
	}
	if _, err := db.ExecContext(ctx, createLoginStatement(login, defaultDatabase)); err != nil {
		return "", err
	}
	if login.Disabled {
		if _, err := db.ExecContext(ctx, "ALTER LOGIN "+quoteName(login.Name)+" DISABLE"); err != nil {
			return "", err
		}
	}
	return "", nil
}

// createLoginStatement inlines the hash and SID (DDL takes no parameters);
// never log it.
func createLoginStatement(login sqlLogin, defaultDatabase string) string {
	onOff := func(value bool) string {
		if value {
			return "ON"
		}
		return "OFF"
	}
	return fmt.Sprintf("CREATE LOGIN %s WITH PASSWORD = 0x%s HASHED, SID = 0x%s, DEFAULT_DATABASE = %s, CHECK_POLICY = %s, CHECK_EXPIRATION = %s",
		quoteName(login.Name), strings.ToUpper(hex.EncodeToString(login.PasswordHash)), strings.ToUpper(hex.EncodeToString(login.SID)),
		quoteName(defaultDatabase), onOff(login.CheckPolicy), onOff(login.CheckExpiration && login.CheckPolicy))
}
