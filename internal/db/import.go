package db

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

// tenantTables get tenant_id = DefaultTenant on import (scripts only when not built-in).
var tenantTables = []string{"keys", "repos", "servers", "scripts", "jobs", "schedules", "triggers", "variables", "custom_metrics",
	"alert_rules", "notifiers", "events", "mute_windows"}

// Settings of the single-owner version that now belong to the owner's user or the organization.
var legacyKeys = []string{"password_hash", "password_login", "totp_secret", "totp_last", "oidc_sub", "oidc_name", "notify_jobs", "seeded_rules"}

// importOrder lists every table of the SQLite schema, parents before children (foreign keys).
var importOrder = []string{"settings", "keys", "repos", "servers", "metrics", "container_metrics", "scripts", "jobs", "job_runs",
	"schedules", "triggers", "variables", "custom_metrics", "custom_values", "alert_rules", "alert_state", "notifiers", "events",
	"mute_windows", "sessions"}

// ImportSQLite copies DATA_DIR/fleetdeck.db (from before Postgres) into the open Postgres database, in one
// transaction: everything goes to the default organization, built-in scripts to the shared library, and the
// single owner becomes a super admin with the login "admin" (signed-in browsers stay signed in).
// It does nothing when there is no such file or Postgres already has data, so it runs on every start and imports once.
func ImportSQLite() error {
	path := filepath.Join(DataDir, "fleetdeck.db")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	var n int
	if err := X.Get(&n, `SELECT (SELECT COUNT(*) FROM servers) + (SELECT COUNT(*) FROM keys) + (SELECT COUNT(*) FROM jobs) + (SELECT COUNT(*) FROM settings)`); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	fmt.Printf("  Importing %s into Postgres…\n", path)
	src, err := sqlx.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer src.Close()

	var tables []string
	if err := src.Select(&tables, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`); err != nil {
		return err
	}
	for _, t := range tables {
		if !slices.Contains(importOrder, t) {
			return fmt.Errorf("unknown table %q in %s: refusing to drop it silently", t, path)
		}
	}

	legacy := map[string]string{}
	if slices.Contains(tables, "settings") {
		var rows []struct {
			Key   string  `db:"key"`
			Value *string `db:"value"`
		}
		if err := src.Select(&rows, `SELECT key, value FROM settings`); err != nil {
			return err
		}
		for _, r := range rows {
			legacy[r.Key] = Str(r.Value)
		}
	}
	owner := ""
	if legacy["password_hash"] != "" {
		owner = NewID()
	}

	counts := map[string]int{}
	err = Tx(func(tx Txn) error {
		if owner != "" {
			totpLast, _ := strconv.ParseInt(cmp.Or(legacy["totp_last"], "-1"), 10, 64)
			pwLogin := 1
			if legacy["password_login"] == "0" {
				pwLogin = 0
			}
			if _, err := tx.Exec(`INSERT INTO users (id, email, name, password_hash, password_login, totp_secret, totp_last, super_admin, last_tenant, created_at)
        VALUES (?, 'admin', 'Admin', ?, ?, ?, ?, 1, ?, ?)`, owner, legacy["password_hash"], pwLogin, nullStr(legacy["totp_secret"]), totpLast, DefaultTenant, Now()); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO memberships (user_id, tenant_id, role, created_at) VALUES (?, ?, 'admin', ?)`, owner, DefaultTenant, Now()); err != nil {
				return err
			}
			if sub := legacy["oidc_sub"]; sub != "" {
				if _, err := tx.Exec(`INSERT INTO identities (provider, subject, user_id, name, created_at) VALUES ('instance', ?, ?, ?, ?)`, sub, owner, legacy["oidc_name"], Now()); err != nil {
					return err
				}
			}
		}
		if v := legacy["notify_jobs"]; v != "" {
			if _, err := tx.Exec(`INSERT INTO tenant_settings (tenant_id, key, value) VALUES (?, 'notify_jobs', ?)`, DefaultTenant, v); err != nil {
				return err
			}
		}
		for _, t := range importOrder {
			if !slices.Contains(tables, t) || (t == "sessions" && owner == "") {
				continue
			}
			rows, err := src.Queryx(`SELECT * FROM "` + t + `"`)
			if err != nil {
				return err
			}
			cols, err := rows.Columns()
			if err != nil {
				rows.Close()
				return err
			}
			var extra []any // values for extra columns, after the SQLite ones
			names := slices.Clone(cols)
			switch {
			case t == "sessions":
				names, extra = append(names, "user_id", "tenant_id"), []any{owner, DefaultTenant}
			case slices.Contains(tenantTables, t):
				names, extra = append(names, "tenant_id"), []any{DefaultTenant}
			}
			q := fmt.Sprintf(`INSERT INTO %s ("%s") VALUES (%s)`, t, strings.Join(names, `", "`), strings.TrimSuffix(strings.Repeat("?, ", len(names)), ", "))
			for rows.Next() {
				vals, err := rows.SliceScan()
				if err != nil {
					rows.Close()
					return err
				}
				if t == "settings" && slices.Contains(legacyKeys, fmt.Sprint(vals[0])) {
					continue
				}
				vals = append(vals, extra...)
				for i, v := range vals {
					switch s := v.(type) {
					case []byte:
						vals[i] = PgText(string(s))
					case string:
						vals[i] = PgText(s)
					}
				}
				if _, err := tx.Exec(q, vals...); err != nil {
					rows.Close()
					return fmt.Errorf("%s: %w (row %v)", t, err, vals[0])
				}
				counts[t]++
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`UPDATE scripts SET tenant_id = NULL WHERE builtin = 1`); err != nil { // the shared library
			return err
		}
		for _, t := range []string{"jobs", "events"} { // identity columns continue after the imported ids
			if _, err := tx.Exec(`SELECT setval(pg_get_serial_sequence('` + t + `', 'id'), COALESCE(MAX(id), 0) + 1, false) FROM ` + t); err != nil {
				return err
			}
		}
		for _, t := range tables { // every row made it
			var want, got int
			q := `SELECT COUNT(*) FROM "` + t + `"`
			switch {
			case t == "settings":
				q += ` WHERE key NOT IN ('` + strings.Join(legacyKeys, "', '") + `')`
			case t == "sessions" && owner == "":
				q += ` WHERE 0`
			}
			if err := src.Get(&want, q); err != nil {
				return err
			}
			if err := tx.Get(&got, `SELECT COUNT(*) FROM `+t); err != nil {
				return err
			}
			if want != got {
				return fmt.Errorf("%s: %d rows in SQLite, %d in Postgres", t, want, got)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, t := range importOrder {
		if c, ok := counts[t]; ok {
			fmt.Printf("  %-18s %d\n", t, c)
		}
	}
	if owner != "" {
		fmt.Println("  Imported into the organization 'default'. Sign in as \"admin\" with your password.")
	} else {
		fmt.Println("  Imported into the organization 'default'.")
	}
	return nil
}

// PgText makes a string storable: Postgres text takes neither NUL bytes nor invalid UTF-8.
func PgText(s string) string { return strings.ReplaceAll(strings.ToValidUTF8(s, "�"), "\x00", "") }
