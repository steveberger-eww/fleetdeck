package web

// Organizations (tenants), users and roles: who signs in, where they work and what they may do.

import (
	"cmp"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"fleetdeck/internal/alerts"
	"fleetdeck/internal/db"
	"fleetdeck/internal/httperr"
	"fleetdeck/internal/sec"
)

type user struct {
	ID            string  `db:"id"`
	Email         string  `db:"email"`
	Name          string  `db:"name"`
	PasswordHash  *string `db:"password_hash"`
	PasswordLogin int     `db:"password_login"`
	TOTPSecret    *string `db:"totp_secret"`
	TOTPLast      int64   `db:"totp_last"`
	SuperAdmin    int     `db:"super_admin"`
	LastTenant    *string `db:"last_tenant"`
	CreatedAt     int64   `db:"created_at"`
}

type tenant struct {
	ID        string `db:"id" json:"id"`
	Name      string `db:"name" json:"name"`
	Slug      string `db:"slug" json:"slug"`
	CreatedAt int64  `db:"created_at" json:"created_at"`
}

func getUser(id string) (*user, error) { return db.Get[user](`SELECT * FROM users WHERE id = ?`, id) }

func userByEmail(email string) (*user, error) {
	return db.Get[user](`SELECT * FROM users WHERE lower(email) = lower(?)`, strings.TrimSpace(email))
}

func getTenant(id string) (*tenant, error) {
	return db.Get[tenant](`SELECT * FROM tenants WHERE id = ?`, id)
}

func b01(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// licensed is where business-license checks go. Features: "tenant-sso" (an organization's own single sign-on).
// ponytail: everything is allowed; check the organization's plan here once there are licenses.
func licensed(tenantID, feature string) bool { return true }

// defaultTenant is where u lands after signing in: their last organization, else their first. Super admins
// without a membership land in the oldest organization.
func defaultTenant(u *user) string {
	t, err := db.Get[string](`SELECT t.id FROM tenants t LEFT JOIN memberships m ON m.tenant_id = t.id AND m.user_id = ?
    WHERE m.user_id IS NOT NULL OR ? ORDER BY (t.id = ?) DESC, m.created_at NULLS LAST, t.created_at LIMIT 1`,
		u.ID, u.SuperAdmin != 0, db.Str(u.LastTenant))
	if err != nil || t == nil {
		return ""
	}
	return *t
}

// roleIn is the user's role in an organization; "admin" for super admins anywhere, "" for none.
func (c *ctx) roleIn(t string) (string, error) {
	if t == "" {
		return "", nil
	}
	r, err := db.Get[string](`SELECT role FROM memberships WHERE user_id = ? AND tenant_id = ?`, c.user.ID, t)
	if err != nil || r != nil {
		return db.Str(r), err
	}
	if c.super {
		if ok, err := db.Get[int](`SELECT 1 FROM tenants WHERE id = ?`, t); err != nil || ok != nil {
			return "admin", err
		}
	}
	return "", nil
}

// load fills user, tenant and role from the session. When the session's organization is gone or the user was
// removed from it, an unlocked session moves on to another of their organizations.
func (c *ctx) load() error {
	u, err := getUser(c.sess.UserID)
	if err != nil || u == nil {
		return err
	}
	c.user, c.super = u, u.SuperAdmin != 0 && c.sess.Locked == 0
	t := db.Str(c.sess.TenantID)
	role, err := c.roleIn(t)
	if err != nil {
		return err
	}
	if role == "" && c.sess.Locked == 0 {
		if t = defaultTenant(u); t != "" {
			if role, err = c.roleIn(t); err != nil {
				return err
			}
			if _, err := db.Exec(`UPDATE sessions SET tenant_id = ? WHERE hash = ?`, t, c.sess.Hash); err != nil {
				return err
			}
		}
	}
	if role != "" {
		c.tenant, c.role = t, role
	}
	return nil
}

// allowed checks the route's flags against the signed-in user.
func (c *ctx) allowed(f flag) error {
	switch {
	case f&open != 0:
		return nil
	case c.user == nil:
		return httperr.New(401, "Not signed in")
	case f&account != 0:
		return nil
	case f&supers != 0:
		if !c.super {
			return httperr.New(403, "Only super admins can do that")
		}
		return nil
	case c.tenant == "":
		return httperr.New(403, "You aren't in an organization yet. Ask an admin for an invite.")
	case f&admins != 0 && c.level() < roleAdmin:
		return httperr.New(403, "Only admins of this organization can do that")
	case c.r.Method != "GET" && c.level() < roleOperator:
		return httperr.New(403, "Viewers can look, not change")
	}
	return nil
}

// unlocked refuses account changes from a session of an organization's own single sign-on: that provider is run
// by the organization, so it must not be able to take over the account (password, 2FA, email).
func (c *ctx) unlocked() error {
	if c.sess != nil && c.sess.Locked != 0 {
		return httperr.New(403, "You signed in with your organization's single sign-on. Sign in with your password or the main single sign-on to change your account.")
	}
	return nil
}

// member is the user id whose assigned servers bound what they see; "" for admins, who see every server.
func (c *ctx) member() string {
	if c.level() >= roleAdmin {
		return ""
	}
	return c.user.ID
}

// server loads a server of the organization that the user sees, and checks their role is at least need.
func (c *ctx) server(id string, need int) (*db.Server, error) {
	m := c.member()
	s, err := db.Get[db.Server](`SELECT * FROM servers WHERE id = ? AND tenant_id = ?
    AND (? = '' OR id IN (SELECT server_id FROM server_members WHERE user_id = ?))`, id, c.tenant, m, m)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, notFound("Server")
	}
	if c.level() < need {
		return nil, httperr.New(403, "Your role can't do that on this server")
	}
	return s, nil
}

// sees is the set of server ids the user sees; nil for admins (all of the organization's).
func (c *ctx) sees() (map[string]bool, error) {
	if c.member() == "" {
		return nil, nil
	}
	ids, err := db.Select[string](`SELECT server_id FROM server_members WHERE user_id = ?`, c.user.ID)
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, err
}

// visibleServers lists the organization's servers the user sees, by name.
func (c *ctx) visibleServers(extra string) ([]db.Server, error) {
	m := c.member()
	return db.Select[db.Server](`SELECT * FROM servers WHERE tenant_id = ? AND (? = '' OR id IN (SELECT server_id FROM server_members WHERE user_id = ?)) `+
		extra+` ORDER BY lower(name)`, c.tenant, m, m)
}

// publicBase is Fleetdeck's own URL (no trailing slash): FLEETDECK_PUBLIC_URL, the setting, or the request's host.
func (s *Server) publicBase(r *http.Request) string {
	if u := cmp.Or(os.Getenv("FLEETDECK_PUBLIC_URL"), db.Setting("public_url", "")); u != "" {
		return strings.TrimRight(u, "/")
	}
	scheme := "http"
	if s.isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + s.hostOf(r)
}

var (
	emailRE = regexp.MustCompile(`^[^\s@]+@[^\s@]+$`)
	slugRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	nonSlug = regexp.MustCompile(`[^a-z0-9]+`)
)

func validEmail(e string) (string, error) {
	e = strings.TrimSpace(e)
	if !emailRE.MatchString(e) || len(e) > 254 {
		return "", httperr.Bad("Enter a valid email address")
	}
	return e, nil
}

func validName(n string, what string) (string, error) {
	n = strings.TrimSpace(n)
	if n == "" || jsLen(n) > 100 {
		return "", httperr.Bad(what + " is required (max 100 characters)")
	}
	return n, nil
}

// slugFor checks a requested slug, or derives a free one from the name. except = the organization being renamed.
func slugFor(requested, name, except string) (string, error) {
	taken := func(s string) (bool, error) {
		ok, err := db.Get[int](`SELECT 1 FROM tenants WHERE slug = ? AND id != ?`, s, except)
		return ok != nil, err
	}
	if requested = strings.TrimSpace(strings.ToLower(requested)); requested != "" {
		if !slugRE.MatchString(requested) {
			return "", httperr.Bad("The short name may use a-z, 0-9 and dashes (max 40)")
		}
		if t, err := taken(requested); err != nil || t {
			return "", cmpErr(err, httperr.New(409, "Another organization already uses "+requested))
		}
		return requested, nil
	}
	base := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	base = strings.TrimRight(jsSlice(cmp.Or(base, "org"), 36), "-")
	for i := 1; ; i++ {
		s := base
		if i > 1 {
			s = fmt.Sprintf("%s-%d", base, i)
		}
		if t, err := taken(s); err != nil || !t {
			return s, err
		}
	}
}

// createTenant adds an organization with the default alert rules.
func createTenant(name, slug string) (*tenant, error) {
	t := &tenant{ID: db.NewID(), Name: name, Slug: slug, CreatedAt: db.Now()}
	if _, err := db.Exec(`INSERT INTO tenants (id, name, slug, created_at) VALUES (?, ?, ?, ?)`, t.ID, t.Name, t.Slug, t.CreatedAt); err != nil {
		return nil, err
	}
	return t, alerts.Seed(t.ID)
}

// createInvite returns the link that lets someone join tenant t with role (valid 7 days).
func (c *ctx) createInvite(t, email, role string) (string, error) {
	tok := sec.Token()
	if _, err := db.Exec(`INSERT INTO invites (token_hash, tenant_id, email, role, created_by, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		sec.SHA256(tok), t, email, role, c.actor(), db.Now()+7*864e5); err != nil {
		return "", err
	}
	return c.s.publicBase(c.r) + "/#/invite/" + tok, nil
}

type invite struct {
	TokenHash string  `db:"token_hash"`
	TenantID  string  `db:"tenant_id"`
	Email     string  `db:"email"`
	Role      string  `db:"role"`
	CreatedBy *string `db:"created_by"`
	ExpiresAt int64   `db:"expires_at"`
}

func getInvite(token string) (*invite, error) {
	return db.Get[invite](`SELECT * FROM invites WHERE token_hash = ? AND expires_at > ?`, sec.SHA256(token), db.Now())
}

// join adds u to the invite's organization (keeping a higher role they already have) and uses up the invite.
func join(u *user, inv *invite) error {
	return db.Tx(func(tx db.Txn) error {
		if _, err := tx.Exec(`INSERT INTO memberships (user_id, tenant_id, role, created_at) VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`,
			u.ID, inv.TenantID, inv.Role, db.Now()); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM invites WHERE token_hash = ?`, inv.TokenHash)
		return err
	})
}

func validRole(r string) error {
	if roleLevel[r] == 0 {
		return httperr.Bad("Role must be admin, owner, operator or viewer")
	}
	return nil
}

func (c *ctx) adminsLeft(t, except string) (int, error) {
	n, err := db.Get[int](`SELECT COUNT(*) FROM memberships WHERE tenant_id = ? AND role = 'admin' AND user_id != ?`, t, except)
	if err != nil {
		return 0, err
	}
	return *n, nil
}

// dummyHash keeps a sign-in with an unknown email as slow as one with a wrong password.
var dummyHash = sync.OnceValue(func() string { return sec.HashPassword(sec.Token()) })

type tenantRole struct {
	ID   string `db:"id" json:"id"`
	Name string `db:"name" json:"name"`
	Slug string `db:"slug" json:"slug"`
	Role string `db:"role" json:"role"`
}

func (s *Server) tenancyRoutes() {
	s.route("GET /api/auth/state", func(c *ctx) (any, error) {
		out := J{"setup": needsSetup(), "authed": c.user != nil, "sso": ssoLabel(instanceSSO())}
		if slug := c.query("org"); slug != "" {
			if t, err := db.Get[tenant](`SELECT * FROM tenants WHERE slug = ?`, strings.ToLower(slug)); err != nil {
				return nil, err
			} else if t != nil {
				p := tenantSSO(t.ID)
				out["org"] = J{"slug": t.Slug, "name": t.Name, "sso": p.configured() && licensed(t.ID, "tenant-sso")}
			}
		}
		return out, nil
	}, open)

	s.route("POST /api/auth/setup", func(c *ctx) (any, error) {
		if !needsSetup() {
			return nil, httperr.New(409, "Already set up")
		}
		if err := s.checkRate(c.ip); err != nil {
			return nil, err
		}
		tok := strings.TrimSpace(c.body.str("token"))
		s.mu.Lock()
		valid := s.setupToken != "" && tok == s.setupToken
		s.mu.Unlock()
		if !valid {
			s.recordFailure(c.ip)
			return nil, httperr.New(401, "Invalid setup token (see the server console)")
		}
		email, err := validEmail(c.body.str("email"))
		if err != nil {
			return nil, err
		}
		name, err := validName(c.body.str("name"), "Your name")
		if err != nil {
			return nil, err
		}
		org, err := validName(cmp.Or(c.body.str("organization"), "Default"), "Organization")
		if err != nil {
			return nil, err
		}
		pw := c.body.str("password")
		if jsLen(pw) < 10 {
			return nil, httperr.Bad("Password must be at least 10 characters")
		}
		s.mu.Lock()
		claimed := s.setupToken != ""
		s.setupToken = ""
		s.mu.Unlock()
		if !claimed { // a concurrent setup won
			return nil, httperr.New(409, "Already set up")
		}
		slug, err := slugFor("", org, db.DefaultTenant)
		if err != nil {
			return nil, err
		}
		u := &user{ID: db.NewID(), Email: email, Name: name, SuperAdmin: 1}
		if err := db.Tx(func(tx db.Txn) error {
			if _, err := tx.Exec(`UPDATE tenants SET name = ?, slug = ? WHERE id = ?`, org, slug, db.DefaultTenant); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO users (id, email, name, password_hash, super_admin, created_at) VALUES (?, ?, ?, ?, 1, ?)`,
				u.ID, email, name, sec.HashPassword(pw), db.Now()); err != nil {
				return err
			}
			_, err := tx.Exec(`INSERT INTO memberships (user_id, tenant_id, role, created_at) VALUES (?, ?, 'admin', ?)`, u.ID, db.DefaultTenant, db.Now())
			return err
		}); err != nil {
			return nil, err
		}
		if n, err := db.Get[int](`SELECT COUNT(*) FROM alert_rules WHERE tenant_id = ?`, db.DefaultTenant); err != nil || *n == 0 {
			if err := cmpErr(err, alerts.Seed(db.DefaultTenant)); err != nil {
				return nil, err
			}
		}
		if err := c.createSession(u, db.DefaultTenant, false); err != nil {
			return nil, err
		}
		c.user, c.tenant = u, db.DefaultTenant
		c.audit("Initial setup completed", "")
		return okResp, nil
	}, open)

	s.route("POST /api/auth/login", func(c *ctx) (any, error) {
		if err := s.checkRate(c.ip); err != nil {
			return nil, err
		}
		if needsSetup() {
			return nil, httperr.New(409, "Setup required")
		}
		email := strings.TrimSpace(c.body.str("email"))
		u, err := userByEmail(email)
		if err != nil {
			return nil, err
		}
		hash := dummyHash()
		if u != nil && u.PasswordHash != nil {
			hash = *u.PasswordHash
		}
		if !sec.VerifyPassword(c.body.str("password"), hash) || u == nil || u.PasswordHash == nil {
			s.recordFailure(c.ip)
			db.Event("security", "Failed login for "+jsSlice(email, 100)+" (password)", db.EventOpts{Level: "warn", IP: c.ip})
			return nil, httperr.New(401, "Wrong email or password")
		}
		if u.PasswordLogin == 0 && os.Getenv("FLEETDECK_PASSWORD_LOGIN") != "1" {
			return nil, httperr.New(403, "Password sign-in is turned off for this account. Use single sign-on.")
		}
		secret, err := totpSecret(u)
		if err != nil {
			return nil, err
		}
		if secret != "" && !c.body.truthy("code") {
			return J{"totp_required": true}, nil
		}
		if ok, err := s.checkTOTP(u, c.body["code"]); err != nil || !ok {
			if err != nil {
				return nil, err
			}
			s.recordFailure(c.ip)
			db.Event("security", "Failed login for "+u.Email+" (2FA code)", db.EventOpts{Level: "warn", IP: c.ip})
			return nil, httperr.New(401, "Invalid 2FA code")
		}
		s.mu.Lock()
		delete(s.failures, c.ip)
		s.mu.Unlock()
		if err := c.createSession(u, "", false); err != nil {
			return nil, err
		}
		db.Event("security", "Logged in", db.EventOpts{Actor: u.Email, IP: c.ip})
		return okResp, nil
	}, open)

	s.route("POST /api/auth/logout", func(c *ctx) (any, error) {
		if c.sess != nil {
			if err := exec(`DELETE FROM sessions WHERE hash = ?`, c.sess.Hash); err != nil {
				return nil, err
			}
		}
		c.w.Header().Set("Set-Cookie", s.sessionCookie(c.r, "", 0))
		return okResp, nil
	}, open)

	// ---- who am I, and where ----
	s.route("GET /api/me", func(c *ctx) (any, error) {
		var list []tenantRole
		var err error
		switch {
		case c.sess.Locked != 0:
			list, err = db.Select[tenantRole](`SELECT t.id, t.name, t.slug, m.role FROM tenants t JOIN memberships m ON m.tenant_id = t.id WHERE m.user_id = ? AND t.id = ?`, c.user.ID, c.tenant)
		case c.super:
			list, err = db.Select[tenantRole](`SELECT t.id, t.name, t.slug, COALESCE(m.role, 'admin') AS role FROM tenants t
        LEFT JOIN memberships m ON m.tenant_id = t.id AND m.user_id = ? ORDER BY lower(t.name)`, c.user.ID)
		default:
			list, err = db.Select[tenantRole](`SELECT t.id, t.name, t.slug, m.role FROM tenants t JOIN memberships m ON m.tenant_id = t.id WHERE m.user_id = ? ORDER BY lower(t.name)`, c.user.ID)
		}
		if err != nil {
			return nil, err
		}
		var cur any
		for _, t := range list {
			if t.ID == c.tenant {
				cur = t
			}
		}
		return J{"user": J{"id": c.user.ID, "email": c.user.Email, "name": c.user.Name, "super_admin": c.super},
			"tenant": cur, "role": c.role, "locked": c.sess.Locked != 0, "tenants": list}, nil
	}, account)

	s.route("POST /api/me/tenant", func(c *ctx) (any, error) {
		if c.sess.Locked != 0 {
			return nil, httperr.New(403, "This sign-in is for one organization only")
		}
		id := c.body.str("id")
		role, err := c.roleIn(id)
		if err != nil || role == "" {
			return nil, cmpErr(err, notFound("Organization"))
		}
		if err := exec(`UPDATE sessions SET tenant_id = ? WHERE hash = ?`, id, c.sess.Hash); err != nil {
			return nil, err
		}
		if err := exec(`UPDATE users SET last_tenant = ? WHERE id = ?`, id, c.user.ID); err != nil {
			return nil, err
		}
		if m, err := db.Get[int](`SELECT 1 FROM memberships WHERE user_id = ? AND tenant_id = ?`, c.user.ID, id); err == nil && m == nil {
			db.Event("security", "Super admin "+c.user.Email+" entered the organization", db.EventOpts{Level: "warn", Actor: c.user.Email, IP: c.ip, TenantID: id})
		}
		return okResp, nil
	}, account)

	// ---- the organization ----
	s.route("PUT /api/tenant", func(c *ctx) (any, error) {
		name, err := validName(c.body.str("name"), "Name")
		if err != nil {
			return nil, err
		}
		cur, err := getTenant(c.tenant)
		if err != nil || cur == nil {
			return nil, cmpErr(err, notFound("Organization"))
		}
		slug, err := slugFor(cmp.Or(c.body.str("slug"), cur.Slug), name, c.tenant)
		if err != nil {
			return nil, err
		}
		if err := exec(`UPDATE tenants SET name = ?, slug = ? WHERE id = ?`, name, slug, c.tenant); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf("Renamed the organization to %q (%s)", name, slug), "")
		return getTenant(c.tenant)
	}, admins)

	// ---- people ----
	s.route("GET /api/members", func(c *ctx) (any, error) {
		members, err := db.Rows(`SELECT u.id, u.email, u.name, m.role, m.created_at,
        (SELECT COUNT(*) FROM server_members sm JOIN servers s ON s.id = sm.server_id WHERE sm.user_id = u.id AND s.tenant_id = m.tenant_id) AS servers,
        EXISTS (SELECT 1 FROM identities i WHERE i.user_id = u.id) AS sso, (u.totp_secret IS NOT NULL) AS totp
      FROM memberships m JOIN users u ON u.id = m.user_id WHERE m.tenant_id = ? ORDER BY lower(u.name), lower(u.email)`, c.tenant)
		if err != nil {
			return nil, err
		}
		invites, err := db.Rows(`SELECT substr(token_hash, 1, 16) AS id, email, role, created_by, expires_at FROM invites
      WHERE tenant_id = ? AND expires_at > ? ORDER BY expires_at DESC`, c.tenant, db.Now())
		return J{"members": members, "invites": invites, "me": c.user.ID}, err
	}, admins)

	s.route("POST /api/members/invites", func(c *ctx) (any, error) {
		email, err := validEmail(c.body.str("email"))
		if err != nil {
			return nil, err
		}
		role := c.body.strOr("role", "operator")
		if err := validRole(role); err != nil {
			return nil, err
		}
		link, err := c.createInvite(c.tenant, email, role)
		if err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf("Invited %s as %s", email, role), "")
		return J{"url": link}, nil
	}, admins)

	s.route("DELETE /api/members/invites/{id}", func(c *ctx) (any, error) {
		if err := exec(`DELETE FROM invites WHERE tenant_id = ? AND substr(token_hash, 1, 16) = ?`, c.tenant, c.param("id")); err != nil {
			return nil, err
		}
		c.audit("Withdrew an invite", "")
		return okResp, nil
	}, admins)

	memberOf := func(c *ctx) (*user, string, error) {
		u, err := getUser(c.param("id"))
		if err != nil || u == nil {
			return nil, "", cmpErr(err, notFound("Member"))
		}
		role, err := db.Get[string](`SELECT role FROM memberships WHERE user_id = ? AND tenant_id = ?`, u.ID, c.tenant)
		if err != nil || role == nil {
			return nil, "", cmpErr(err, notFound("Member"))
		}
		return u, *role, nil
	}
	s.route("PUT /api/members/{id}", func(c *ctx) (any, error) {
		u, old, err := memberOf(c)
		if err != nil {
			return nil, err
		}
		role := c.body.str("role")
		if err := validRole(role); err != nil {
			return nil, err
		}
		if old == "admin" && role != "admin" {
			if n, err := c.adminsLeft(c.tenant, u.ID); err != nil || n == 0 {
				return nil, cmpErr(err, httperr.New(409, "An organization needs at least one admin"))
			}
		}
		if err := exec(`UPDATE memberships SET role = ? WHERE user_id = ? AND tenant_id = ?`, role, u.ID, c.tenant); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf("Changed the role of %s from %s to %s", u.Email, old, role), "")
		return okResp, nil
	}, admins)

	s.route("DELETE /api/members/{id}", func(c *ctx) (any, error) {
		u, old, err := memberOf(c)
		if err != nil {
			return nil, err
		}
		if old == "admin" {
			if n, err := c.adminsLeft(c.tenant, u.ID); err != nil || n == 0 {
				return nil, cmpErr(err, httperr.New(409, "An organization needs at least one admin"))
			}
		}
		if err := db.Tx(func(tx db.Txn) error {
			for _, q := range []string{
				`DELETE FROM memberships WHERE user_id = ? AND tenant_id = ?`,
				`DELETE FROM server_members WHERE user_id = ? AND server_id IN (SELECT id FROM servers WHERE tenant_id = ?)`,
				`DELETE FROM sessions WHERE user_id = ? AND tenant_id = ? AND locked = 1`,
				`UPDATE sessions SET tenant_id = NULL WHERE user_id = ? AND tenant_id = ?`,
			} {
				if _, err := tx.Exec(q, u.ID, c.tenant); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
		c.audit("Removed "+u.Email+" from the organization", "")
		return okResp, nil
	}, admins)

	// ---- who sees a server ----
	s.route("GET /api/servers/{id}/members", func(c *ctx) (any, error) {
		srv, err := c.server(c.param("id"), roleOwner)
		if err != nil {
			return nil, err
		}
		return db.Rows(`SELECT u.id, u.email, u.name, m.role, EXISTS (SELECT 1 FROM server_members sm WHERE sm.server_id = ? AND sm.user_id = u.id) AS assigned
      FROM memberships m JOIN users u ON u.id = m.user_id WHERE m.tenant_id = ? AND m.role != 'admin' ORDER BY lower(u.name), lower(u.email)`, srv.ID, c.tenant)
	})

	s.route("PUT /api/servers/{id}/members", func(c *ctx) (any, error) {
		srv, err := c.server(c.param("id"), roleOwner)
		if err != nil {
			return nil, err
		}
		arr, _ := c.body["user_ids"].([]any)
		if err := db.Tx(func(tx db.Txn) error {
			if _, err := tx.Exec(`DELETE FROM server_members WHERE server_id = ?`, srv.ID); err != nil {
				return err
			}
			for _, x := range arr {
				// Only members of this organization below admin (admins see every server anyway).
				if _, err := tx.Exec(`INSERT INTO server_members (server_id, user_id) SELECT ?, user_id FROM memberships
          WHERE user_id = ? AND tenant_id = ? AND role != 'admin' ON CONFLICT DO NOTHING`, srv.ID, jsString(x), c.tenant); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
		c.audit(fmt.Sprintf("Changed who can see %s (%d people)", srv.Name, len(arr)), srv.ID)
		return okResp, nil
	})

	// ---- invites (the link is the credential) ----
	s.route("GET /api/invites/{token}", func(c *ctx) (any, error) {
		inv, err := getInvite(c.param("token"))
		if err != nil || inv == nil {
			return nil, cmpErr(err, httperr.New(404, "This invite link is invalid or has expired. Ask for a new one."))
		}
		t, err := getTenant(inv.TenantID)
		if err != nil || t == nil {
			return nil, cmpErr(err, notFound("Organization"))
		}
		out := J{"org": t.Name, "email": inv.Email, "role": inv.Role, "user": nil, "sso": ssoLabel(instanceSSO())}
		if c.user != nil {
			out["user"] = c.user.Email
		}
		if u, err := userByEmail(inv.Email); err != nil {
			return nil, err
		} else {
			out["exists"] = u != nil
		}
		return out, nil
	}, open)

	s.route("POST /api/invites/{token}/accept", func(c *ctx) (any, error) {
		if err := s.checkRate(c.ip); err != nil {
			return nil, err
		}
		inv, err := getInvite(c.param("token"))
		if err != nil || inv == nil {
			s.recordFailure(c.ip)
			return nil, cmpErr(err, httperr.New(404, "This invite link is invalid or has expired. Ask for a new one."))
		}
		if c.user != nil { // signed in: join with this account
			if err := c.unlocked(); err != nil {
				return nil, err
			}
			if err := join(c.user, inv); err != nil {
				return nil, err
			}
			if err := exec(`UPDATE sessions SET tenant_id = ? WHERE hash = ?`, inv.TenantID, c.sess.Hash); err != nil {
				return nil, err
			}
			db.Event("audit", fmt.Sprintf("%s joined as %s", c.user.Email, inv.Role), db.EventOpts{Actor: c.user.Email, IP: c.ip, TenantID: inv.TenantID})
			return okResp, nil
		}
		if u, err := userByEmail(inv.Email); err != nil || u != nil {
			return nil, cmpErr(err, httperr.New(409, "There is already an account for "+inv.Email+". Sign in, then open this link again."))
		}
		name, err := validName(c.body.str("name"), "Your name")
		if err != nil {
			return nil, err
		}
		pw := c.body.str("password")
		if jsLen(pw) < 10 {
			return nil, httperr.Bad("Password must be at least 10 characters")
		}
		u := &user{ID: db.NewID(), Email: inv.Email, Name: name}
		if err := exec(`INSERT INTO users (id, email, name, password_hash, created_at) VALUES (?, ?, ?, ?, ?)`, u.ID, u.Email, name, sec.HashPassword(pw), db.Now()); err != nil {
			return nil, err
		}
		if err := join(u, inv); err != nil {
			return nil, err
		}
		if err := c.createSession(u, inv.TenantID, false); err != nil {
			return nil, err
		}
		db.Event("audit", fmt.Sprintf("%s created an account and joined as %s", u.Email, inv.Role), db.EventOpts{Actor: u.Email, IP: c.ip, TenantID: inv.TenantID})
		return okResp, nil
	}, open)

	// ---- your account ----
	s.route("GET /api/account", func(c *ctx) (any, error) {
		list, err := db.Select[session](`SELECT * FROM sessions WHERE user_id = ? ORDER BY seen_at DESC`, c.user.ID)
		if err != nil {
			return nil, err
		}
		out := []J{}
		for _, x := range list {
			out = append(out, J{"id": x.Hash[:16], "created_at": x.CreatedAt, "seen_at": x.SeenAt, "ip": x.IP, "ua": x.UA, "current": x.Hash == c.sess.Hash})
		}
		passkeys, err := db.Select[passkeyRow](`SELECT * FROM passkeys WHERE user_id = ? ORDER BY created_at`, c.user.ID)
		return J{"email": c.user.Email, "name": c.user.Name, "totp": c.user.TOTPSecret != nil, "has_password": c.user.PasswordHash != nil,
			"locked": c.sess.Locked != 0, "sessions": out, "passkeys": passkeys}, err
	}, account)

	s.route("PUT /api/account", func(c *ctx) (any, error) {
		if err := c.unlocked(); err != nil {
			return nil, err
		}
		name, err := validName(c.body.str("name"), "Name")
		if err != nil {
			return nil, err
		}
		email := c.user.Email
		if e := strings.TrimSpace(c.body.str("email")); e != "" && e != email {
			if email, err = validEmail(e); err != nil {
				return nil, err
			}
			if other, err := userByEmail(email); err != nil || (other != nil && other.ID != c.user.ID) {
				return nil, cmpErr(err, httperr.New(409, "Another account uses "+email))
			}
		}
		if err := exec(`UPDATE users SET name = ?, email = ? WHERE id = ?`, name, email, c.user.ID); err != nil {
			return nil, err
		}
		if email != c.user.Email {
			db.Event("security", fmt.Sprintf("Changed the sign-in email from %s to %s", c.user.Email, email), db.EventOpts{Actor: email, IP: c.ip})
		}
		return okResp, nil
	}, account)

	s.route("POST /api/account/password", func(c *ctx) (any, error) {
		if err := c.unlocked(); err != nil {
			return nil, err
		}
		if err := s.checkRate(c.ip); err != nil {
			return nil, err
		}
		// Accounts made by single sign-on have no password yet: the (unlocked) session is proof enough to add one.
		if c.user.PasswordHash != nil && !sec.VerifyPassword(c.body.str("current"), *c.user.PasswordHash) {
			s.recordFailure(c.ip)
			return nil, httperr.New(401, "Current password is wrong")
		}
		next := c.body.str("next")
		if jsLen(next) < 10 {
			return nil, httperr.Bad("New password must be at least 10 characters")
		}
		if err := exec(`UPDATE users SET password_hash = ? WHERE id = ?`, sec.HashPassword(next), c.user.ID); err != nil {
			return nil, err
		}
		if err := exec(`DELETE FROM sessions WHERE user_id = ? AND hash != ?`, c.user.ID, c.sess.Hash); err != nil {
			return nil, err
		}
		db.Event("security", "Password changed; other sessions signed out", db.EventOpts{Actor: c.user.Email, IP: c.ip})
		return okResp, nil
	}, account)

	s.route("POST /api/account/totp/begin", func(c *ctx) (any, error) {
		if err := c.unlocked(); err != nil {
			return nil, err
		}
		if c.user.TOTPSecret != nil {
			return nil, httperr.New(409, "Two-factor sign-in is already on. Turn it off first (needs password and code).")
		}
		secret := sec.NewTOTPSecret()
		s.mu.Lock()
		s.pendingTOTP[c.sess.Hash] = secret
		s.mu.Unlock()
		uri := "otpauth://totp/Fleetdeck:" + url.PathEscape(c.user.Email) + "?secret=" + secret + "&issuer=Fleetdeck&algorithm=SHA1&digits=6&period=30"
		img, err := qrDataURI(uri)
		return J{"secret": secret, "uri": uri, "qr": img}, err
	}, account)

	s.route("POST /api/account/totp/enable", func(c *ctx) (any, error) {
		if err := c.unlocked(); err != nil {
			return nil, err
		}
		s.mu.Lock()
		secret := s.pendingTOTP[c.sess.Hash]
		s.mu.Unlock()
		if secret == "" {
			return nil, httperr.Bad("Start 2FA setup first")
		}
		if _, ok := sec.VerifyTOTP(secret, strings.Join(strings.Fields(c.body.str("code")), ""), time.Now()); !ok {
			return nil, httperr.Bad("Code does not match, check your device clock")
		}
		if err := exec(`UPDATE users SET totp_secret = ?, totp_last = -1 WHERE id = ?`, sec.Encrypt(secret), c.user.ID); err != nil {
			return nil, err
		}
		s.mu.Lock()
		delete(s.pendingTOTP, c.sess.Hash)
		s.mu.Unlock()
		db.Event("security", "Two-factor authentication enabled", db.EventOpts{Actor: c.user.Email, IP: c.ip})
		return okResp, nil
	}, account)

	s.route("POST /api/account/totp/disable", func(c *ctx) (any, error) {
		if err := c.unlocked(); err != nil {
			return nil, err
		}
		if err := s.checkRate(c.ip); err != nil {
			return nil, err
		}
		ok := c.user.PasswordHash != nil && sec.VerifyPassword(c.body.str("password"), *c.user.PasswordHash)
		if ok {
			var err error
			if ok, err = s.checkTOTP(c.user, c.body["code"]); err != nil {
				return nil, err
			}
		}
		if !ok {
			s.recordFailure(c.ip)
			return nil, httperr.New(401, "Password or code is wrong")
		}
		if err := exec(`UPDATE users SET totp_secret = NULL WHERE id = ?`, c.user.ID); err != nil {
			return nil, err
		}
		db.Event("security", "Two-factor authentication disabled", db.EventOpts{Level: "warn", Actor: c.user.Email, IP: c.ip})
		return okResp, nil
	}, account)

	s.route("DELETE /api/account/sessions/{id}", func(c *ctx) (any, error) {
		r, err := db.Exec(`DELETE FROM sessions WHERE user_id = ? AND substr(hash, 1, 16) = ? AND hash != ?`, c.user.ID, c.param("id"), c.sess.Hash)
		if err != nil {
			return nil, err
		}
		n, _ := r.RowsAffected()
		return J{"ok": n > 0}, nil
	}, account)

	s.route("POST /api/account/sessions/revoke-others", func(c *ctx) (any, error) {
		if err := exec(`DELETE FROM sessions WHERE user_id = ? AND hash != ?`, c.user.ID, c.sess.Hash); err != nil {
			return nil, err
		}
		db.Event("security", "Signed out all other sessions", db.EventOpts{Actor: c.user.Email, IP: c.ip})
		return okResp, nil
	}, account)

	// ---- the instance (super admins) ----
	s.route("GET /api/instance", func(c *ctx) (any, error) {
		tenants, err := db.Rows(`SELECT t.id, t.name, t.slug, t.created_at,
        (SELECT COUNT(*) FROM servers s WHERE s.tenant_id = t.id) AS servers,
        (SELECT COUNT(*) FROM memberships m WHERE m.tenant_id = t.id) AS members
      FROM tenants t ORDER BY lower(t.name)`)
		if err != nil {
			return nil, err
		}
		users, err := db.Rows(`SELECT u.id, u.email, u.name, u.super_admin, u.created_at,
        COALESCE((SELECT string_agg(t.name || ' (' || m.role || ')', ', ' ORDER BY lower(t.name)) FROM memberships m JOIN tenants t ON t.id = m.tenant_id WHERE m.user_id = u.id), '') AS orgs
      FROM users u ORDER BY lower(u.email)`)
		if err != nil {
			return nil, err
		}
		out := J{"public_url": db.Setting("public_url", ""), "public_url_env": os.Getenv("FLEETDECK_PUBLIC_URL"),
			"secret_from_env": os.Getenv("FLEETDECK_SECRET") != "", "tenants": tenants, "users": users, "me": c.user.ID}
		for _, r := range settingRanges {
			out[r.key] = jsNumber(db.Setting(r.key, fmt.Sprint(r.value)))
		}
		return out, nil
	}, supers)

	s.route("PUT /api/instance", func(c *ctx) (any, error) {
		if v, ok := c.body["public_url"]; ok {
			u := strings.TrimRight(strings.TrimSpace(jsString(v)), "/")
			if u != "" {
				p, err := url.Parse(u)
				if err != nil || (p.Scheme != "https" && p.Scheme != "http") || p.Host == "" || p.RawQuery != "" || p.Fragment != "" {
					return nil, httperr.Bad("Public URL must look like https://fleetdeck.example.com")
				}
			}
			if err := db.SetSetting("public_url", nullIfEmpty(u)); err != nil {
				return nil, err
			}
		}
		for _, r := range settingRanges {
			if !c.body.has(r.key) {
				continue
			}
			v := float64(int64(c.body.num(r.key) + 0.5))
			if !(v >= r.lo && v <= r.hi) {
				return nil, httperr.Bad(fmt.Sprintf("%s must be %d-%d", r.key, int(r.lo), int(r.hi)))
			}
			if err := db.SetSetting(r.key, int64(v)); err != nil {
				return nil, err
			}
		}
		db.Event("audit", "Changed instance settings", db.EventOpts{Actor: c.user.Email, IP: c.ip})
		return okResp, nil
	}, supers)

	s.route("POST /api/instance/tenants", func(c *ctx) (any, error) {
		name, err := validName(c.body.str("name"), "Name")
		if err != nil {
			return nil, err
		}
		slug, err := slugFor(c.body.str("slug"), name, "")
		if err != nil {
			return nil, err
		}
		var email string
		if e := c.body.str("admin_email"); e != "" {
			if email, err = validEmail(e); err != nil {
				return nil, err
			}
		}
		t, err := createTenant(name, slug)
		if err != nil {
			return nil, err
		}
		db.Event("audit", fmt.Sprintf("Created the organization %q (%s)", name, slug), db.EventOpts{Actor: c.user.Email, IP: c.ip})
		out := J{"tenant": t}
		if email != "" {
			if out["invite_url"], err = c.createInvite(t.ID, email, "admin"); err != nil {
				return nil, err
			}
		}
		return out, nil
	}, supers)

	s.route("PUT /api/instance/tenants/{id}", func(c *ctx) (any, error) {
		t, err := getTenant(c.param("id"))
		if err != nil || t == nil {
			return nil, cmpErr(err, notFound("Organization"))
		}
		name, err := validName(c.body.strOr("name", t.Name), "Name")
		if err != nil {
			return nil, err
		}
		slug, err := slugFor(c.body.strOr("slug", t.Slug), name, t.ID)
		if err != nil {
			return nil, err
		}
		if err := exec(`UPDATE tenants SET name = ?, slug = ? WHERE id = ?`, name, slug, t.ID); err != nil {
			return nil, err
		}
		db.Event("audit", fmt.Sprintf("Renamed the organization %q to %q (%s)", t.Name, name, slug), db.EventOpts{Actor: c.user.Email, IP: c.ip})
		return getTenant(t.ID)
	}, supers)

	s.route("DELETE /api/instance/tenants/{id}", func(c *ctx) (any, error) {
		t, err := getTenant(c.param("id"))
		if err != nil || t == nil {
			return nil, cmpErr(err, notFound("Organization"))
		}
		if n, err := db.Get[int](`SELECT COUNT(*) FROM servers WHERE tenant_id = ?`, t.ID); err != nil || *n > 0 {
			return nil, cmpErr(err, httperr.New(409, fmt.Sprintf("%s still has %d servers. Remove them first.", t.Name, *n)))
		}
		if n, err := db.Get[int](`SELECT COUNT(*) FROM tenants`); err != nil || *n <= 1 {
			return nil, cmpErr(err, httperr.New(409, "This is the last organization"))
		}
		if err := exec(`DELETE FROM tenants WHERE id = ?`, t.ID); err != nil { // everything it owns goes with it
			return nil, err
		}
		db.Event("audit", fmt.Sprintf("Deleted the organization %q", t.Name), db.EventOpts{Level: "warn", Actor: c.user.Email, IP: c.ip})
		return okResp, nil
	}, supers)

	supersLeft := func(except string) (int, error) {
		n, err := db.Get[int](`SELECT COUNT(*) FROM users WHERE super_admin = 1 AND id != ?`, except)
		if err != nil {
			return 0, err
		}
		return *n, nil
	}
	s.route("PUT /api/instance/users/{id}", func(c *ctx) (any, error) {
		u, err := getUser(c.param("id"))
		if err != nil || u == nil {
			return nil, cmpErr(err, notFound("User"))
		}
		on := c.body.truthy("super_admin")
		if !on {
			if n, err := supersLeft(u.ID); err != nil || n == 0 {
				return nil, cmpErr(err, httperr.New(409, "There must be at least one super admin"))
			}
		}
		if err := exec(`UPDATE users SET super_admin = ? WHERE id = ?`, b01(on), u.ID); err != nil {
			return nil, err
		}
		db.Event("security", fmt.Sprintf("%s super admin rights for %s", map[bool]string{true: "Granted", false: "Removed"}[on], u.Email),
			db.EventOpts{Level: "warn", Actor: c.user.Email, IP: c.ip})
		return okResp, nil
	}, supers)

	s.route("DELETE /api/instance/users/{id}", func(c *ctx) (any, error) {
		u, err := getUser(c.param("id"))
		if err != nil || u == nil {
			return nil, cmpErr(err, notFound("User"))
		}
		if u.ID == c.user.ID {
			return nil, httperr.Bad("You can't delete your own account")
		}
		// Organizations would be left without anyone to run them.
		orphan, err := db.Get[string](`SELECT t.name FROM memberships m JOIN tenants t ON t.id = m.tenant_id WHERE m.user_id = ? AND m.role = 'admin'
      AND NOT EXISTS (SELECT 1 FROM memberships o WHERE o.tenant_id = m.tenant_id AND o.role = 'admin' AND o.user_id != m.user_id) LIMIT 1`, u.ID)
		if err != nil || orphan != nil {
			return nil, cmpErr(err, httperr.New(409, fmt.Sprintf("%s is the only admin of %s. Make someone else admin first.", u.Email, db.Str(orphan))))
		}
		if err := exec(`DELETE FROM users WHERE id = ?`, u.ID); err != nil {
			return nil, err
		}
		db.Event("security", "Deleted the account "+u.Email, db.EventOpts{Level: "warn", Actor: c.user.Email, IP: c.ip})
		return okResp, nil
	}, supers)
}
