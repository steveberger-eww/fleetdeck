// Package repos: Semaphore-style Git repositories + Ansible playbook runs.
// Repos are shallow clones under DATA_DIR/repos, synced before every run. Ansible runs on the Fleetdeck host,
// with an inventory built from Fleetdeck's servers and known_hosts built from Fleetdeck's pinned host keys.
package repos

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"fleetdeck/internal/db"
	"fleetdeck/internal/httperr"
	"fleetdeck/internal/sec"
	"fleetdeck/internal/ssh"
)

// HostKeyChanged starts the error of a sync that failed because the Git server's host key changed.
const HostKeyChanged = "Host key verification failed"

func reposDir() string      { return filepath.Join(db.DataDir, "repos") }
func gitKnownHosts() string { return filepath.Join(db.DataDir, "git_known_hosts") }
func repoDir(id string) string {
	return filepath.Join(reposDir(), id)
}

// Private scratch space for decrypted keys, inventories and vars. Wiped on start, so a crash mid-run
// never leaves a decrypted key behind.
func tmpDir() string { return filepath.Join(db.DataDir, "tmp") }

// Init wipes and recreates the private temp dir. Call once at startup, after db.Open.
func Init() error {
	if err := os.RemoveAll(tmpDir()); err != nil {
		return err
	}
	return os.MkdirAll(tmpDir(), 0o700)
}

func tempDir(prefix string) (string, error) { return os.MkdirTemp(tmpDir(), prefix+"*") }

func lastLine(s string) string {
	ls := slices.DeleteFunc(strings.Split(strings.TrimSpace(s), "\n"), func(l string) bool { return l == "" })
	if len(ls) == 0 {
		return ""
	}
	return ls[len(ls)-1]
}

var (
	gitErrLine   = regexp.MustCompile(`(?i)Host key verification failed|Permission denied|fatal:|error:|not found|Could not resolve`)
	gitErrIgnore = regexp.MustCompile(`Could not read from remote`)
)

// The useful line of a git failure, not git's generic closing advice.
func gitError(s string) string {
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if gitErrLine.MatchString(l) && !gitErrIgnore.MatchString(l) {
			return l
		}
	}
	return lastLine(s)
}

// ---------- validation ----------
var (
	urlRE      = regexp.MustCompile(`^(https://[A-Za-z0-9.-]+(:\d+)?/\S+|ssh://[\w.-]+@[A-Za-z0-9.-]+(:\d+)?/\S+|[\w.-]+@[A-Za-z0-9.-]+:[\w~][\w./~-]*)$`)
	branchRE   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]{0,199}$`)
	playbookRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]*\.ya?ml$`)
	userRE     = regexp.MustCompile(`^[\w.@-]{1,100}$`)
)

// Repo is a row of the repos table.
type Repo struct {
	ID         string  `db:"id"`
	Name       string  `db:"name"`
	URL        string  `db:"url"`
	Branch     string  `db:"branch"`
	Auth       string  `db:"auth"`
	KeyID      *string `db:"key_id"`
	Username   *string `db:"username"`
	TokenEnc   *string `db:"token_enc"`
	LastSync   *int64  `db:"last_sync"`
	LastCommit *string `db:"last_commit"`
	LastError  *string `db:"last_error"`
	CreatedAt  int64   `db:"created_at"`
}

// RepoInput is the request body of create/update. For an update, prefill it from the existing row, then decode
// the body over it (the JS merged {...old, ...body}).
type RepoInput struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Branch   string `json:"branch"`
	Auth     string `json:"auth"`
	KeyID    string `json:"key_id"`
	Username string `json:"username"`
	Token    string `json:"token"`
}

// ValidateRepo returns the columns to store (name, url, branch, auth, key_id, username, token_enc).
func ValidateRepo(b RepoInput, existing *Repo) (*Repo, error) {
	name := strings.TrimSpace(b.Name)
	url := strings.TrimSpace(b.URL)
	branch := strings.TrimSpace(cmp.Or(b.Branch, "main"))
	auth := cmp.Or(b.Auth, "none")
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return nil, httperr.Bad("Name is required")
	}
	// IndexFunc: JS's \S also excludes Unicode spaces, RE2's only ASCII ones.
	if !urlRE.MatchString(url) || strings.IndexFunc(url, unicode.IsSpace) >= 0 || len(url) > 500 {
		return nil, httperr.Bad("Use an https:// URL, ssh://user@host/path, or user@host:path")
	}
	if !branchRE.MatchString(branch) || strings.Contains(branch, "..") {
		return nil, httperr.Bad("Invalid branch name")
	}
	if !slices.Contains([]string{"none", "ssh", "token"}, auth) {
		return nil, httperr.Bad("Invalid authentication type")
	}
	isHTTPS := strings.HasPrefix(url, "https://")
	out := &Repo{Name: name, URL: url, Branch: branch, Auth: auth}
	if auth == "ssh" {
		if isHTTPS {
			return nil, httperr.Bad("SSH key authentication needs an SSH URL (git@host:org/repo.git)")
		}
		if ok, err := exists(`SELECT 1 FROM keys WHERE id = ?`, b.KeyID); err != nil {
			return nil, err
		} else if !ok {
			return nil, httperr.Bad("Pick an SSH key")
		}
		out.KeyID = &b.KeyID
	}
	if auth == "token" {
		if !isHTTPS {
			return nil, httperr.Bad("Token authentication needs an https:// URL")
		}
		u := strings.TrimSpace(cmp.Or(b.Username, "git"))
		if !userRE.MatchString(u) {
			return nil, httperr.Bad("Invalid username")
		}
		out.Username = &u
		switch {
		case b.Token != "":
			enc := sec.Encrypt(b.Token)
			out.TokenEnc = &enc
		case existing != nil && existing.TokenEnc != nil:
			out.TokenEnc = existing.TokenEnc // blank on edit = keep
		default:
			return nil, httperr.Bad("Access token is required")
		}
	}
	return out, nil
}

func exists(q string, args ...any) (bool, error) {
	r, err := db.Get[int](q, args...)
	return r != nil, err
}

// ValidPlaybook: a relative .yml/.yaml path that stays inside the repo.
func ValidPlaybook(p string) bool {
	return playbookRE.MatchString(p) && !slices.Contains(strings.Split(p, "/"), "..")
}

// ---------- process helper ----------

// deliberately not os.Environ(): FLEETDECK_SECRET must not reach git or playbooks
func baseEnv() map[string]string {
	home, _ := os.UserHomeDir()
	env := map[string]string{"PATH": os.Getenv("PATH"), "HOME": cmp.Or(os.Getenv("HOME"), home), "LANG": cmp.Or(os.Getenv("LANG"), "C.UTF-8")}
	if v := os.Getenv("SSH_AUTH_SOCK"); v != "" {
		env["SSH_AUTH_SOCK"] = v
	}
	return env
}

type shOpts struct {
	dir     string
	env     map[string]string // nil = baseEnv()
	onData  func(string)
	timeout time.Duration // 0 = 30 min
}

// collector gathers stdout+stderr (one pipe) and forwards chunks; the mutex also orders the timeout note.
type collector struct {
	mu     sync.Mutex
	out    strings.Builder
	onData func(string)
}

func (c *collector) Write(p []byte) (int, error) { c.add(string(p)); return len(p), nil }
func (c *collector) add(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.out.Len() < 4_000_000 {
		c.out.WriteString(s)
	}
	if c.onData != nil {
		c.onData(s)
	}
}

// sh runs a command in its own process group; a timeout or ctx cancellation kills the whole group.
// It never fails: a missing binary is exit code 127 with an explanation as output.
func sh(ctx context.Context, name string, args []string, o shOpts) (int, string) {
	if o.env == nil {
		o.env = baseEnv()
	}
	if o.timeout == 0 {
		o.timeout = 30 * time.Minute
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = o.dir
	for k, v := range o.env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	c := &collector{onData: o.onData}
	cmd.Stdout, cmd.Stderr = c, c
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 10 * time.Second // a daemonized grandchild holding the pipe must not hang the run
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return 127, name + " is not installed on the Fleetdeck host"
		}
		return 127, err.Error()
	}
	kill := func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	timer := time.AfterFunc(o.timeout, func() {
		c.add(fmt.Sprintf("\n[timed out after %s min]\n", strconv.FormatFloat(o.timeout.Minutes(), 'f', -1, 64)))
		kill()
	})
	stop := context.AfterFunc(ctx, kill)
	_ = cmd.Wait()
	timer.Stop()
	stop()
	code := 1
	if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() >= 0 {
		code = cmd.ProcessState.ExitCode()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return code, c.out.String()
}

// Serialize work per repo so a sync never rewrites files under a running playbook.
// ponytail: one run per repo at a time; copy the checkout per job if you need parallel runs from one repo.
var (
	locksMu sync.Mutex
	locks   = map[string]chan struct{}{}
)

func lock(ctx context.Context, id string) (unlock func(), err error) {
	locksMu.Lock()
	l := locks[id]
	if l == nil {
		l = make(chan struct{}, 1)
		locks[id] = l
	}
	locksMu.Unlock()
	select {
	case l <- struct{}{}:
		return func() { <-l }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Decrypts a stored key to a 0600 file inside a private temp dir (removed after the run).
func writeKey(dir, keyID string) (string, error) {
	file := filepath.Join(dir, "key-"+keyID)
	if _, err := os.Stat(file); err == nil {
		return file, nil
	}
	type keyRow struct {
		PrivateEnc    string  `db:"private_enc"`
		PassphraseEnc *string `db:"passphrase_enc"`
	}
	k, err := db.Get[keyRow](`SELECT private_enc, passphrase_enc FROM keys WHERE id = ?`, keyID)
	if err != nil {
		return "", err
	}
	if k == nil {
		return "", errors.New("An assigned SSH key no longer exists")
	}
	pem, err := sec.Decrypt(k.PrivateEnc)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(pem, "\n") {
		pem += "\n"
	}
	if err := os.WriteFile(file, []byte(pem), 0o600); err != nil {
		return "", err
	}
	if k.PassphraseEnc != nil {
		pass, err := sec.Decrypt(*k.PassphraseEnc)
		if err != nil {
			return "", err
		}
		// ponytail: the passphrase is briefly visible to local `ps`; prefer keys without passphrase (Fleetdeck encrypts them at rest).
		if code, _ := sh(context.Background(), "ssh-keygen", []string{"-p", "-q", "-P", pass, "-N", "", "-f", file}, shOpts{}); code != 0 {
			return "", errors.New("Could not unlock the passphrase-protected key")
		}
	}
	return file, nil
}

// ---------- git ----------

func gitEnv(repo *Repo, tmp string) (map[string]string, error) {
	cfg := [][2]string{{"core.hooksPath", "/dev/null"}, {"protocol.file.allow", "never"}, {"submodule.recurse", "false"}}
	if repo.Auth == "token" {
		token, err := sec.Decrypt(db.Str(repo.TokenEnc))
		if err != nil {
			return nil, err
		}
		cfg = append(cfg, [2]string{"http.extraHeader", "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(db.Str(repo.Username)+":"+token))})
	}
	env := baseEnv()
	env["GIT_TERMINAL_PROMPT"], env["GIT_ALLOW_PROTOCOL"], env["GIT_CONFIG_NOSYSTEM"] = "0", "https:ssh", "1"
	env["GIT_CONFIG_COUNT"] = strconv.Itoa(len(cfg))
	for i, kv := range cfg { // via env, so tokens never show up in argv
		env[fmt.Sprintf("GIT_CONFIG_KEY_%d", i)], env[fmt.Sprintf("GIT_CONFIG_VALUE_%d", i)] = kv[0], kv[1]
	}
	key := ""
	if repo.Auth == "ssh" {
		f, err := writeKey(tmp, db.Str(repo.KeyID))
		if err != nil {
			return nil, err
		}
		key = "-i " + ssh.Shq(f) + " -o IdentitiesOnly=yes "
	}
	env["GIT_SSH_COMMAND"] = "ssh " + key + "-o BatchMode=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=" + ssh.Shq(gitKnownHosts())
	return env, nil
}

func syncUnlocked(repo *Repo, onData func(string)) (string, error) {
	commit, err := gitSync(repo, onData)
	if err != nil {
		if _, e := db.Exec(`UPDATE repos SET last_sync = ?, last_error = ? WHERE id = ?`, db.Now(), err.Error(), repo.ID); e != nil {
			return "", e
		}
		return "", err
	}
	_, err = db.Exec(`UPDATE repos SET last_sync = ?, last_commit = ?, last_error = NULL WHERE id = ?`, db.Now(), commit, repo.ID)
	return commit, err
}

func gitSync(repo *Repo, onData func(string)) (string, error) {
	// Not cancellable, like the JS: a cancelled job still lets an ongoing fetch finish.
	bg := context.Background()
	dir := repoDir(repo.ID)
	tmp, err := tempDir("git-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	env, err := gitEnv(repo, tmp)
	if err != nil {
		return "", err
	}
	o := shOpts{env: env, onData: onData}
	var code int
	var out string
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if err := os.MkdirAll(reposDir(), 0o700); err != nil {
			return "", err
		}
		if err := os.RemoveAll(dir); err != nil {
			return "", err
		}
		onData(fmt.Sprintf("$ git clone --depth 1 --branch %s %s\n", repo.Branch, repo.URL))
		code, out = sh(bg, "git", []string{"clone", "--depth", "1", "--branch", repo.Branch, "--single-branch", "--no-recurse-submodules", "--", repo.URL, dir}, o)
	} else {
		onData(fmt.Sprintf("$ git fetch --depth 1 origin %s\n", repo.Branch))
		code, out = sh(bg, "git", []string{"-C", dir, "fetch", "--depth", "1", "origin", repo.Branch}, o)
		if code == 0 {
			code, out = sh(bg, "git", []string{"-C", dir, "reset", "-q", "--hard", "FETCH_HEAD"}, o)
		}
		if code == 0 {
			code, out = sh(bg, "git", []string{"-C", dir, "clean", "-q", "-fdx", "-e", ".fleetdeck"}, o)
		}
	}
	if code != 0 {
		if strings.Contains(out, "REMOTE HOST IDENTIFICATION HAS CHANGED") {
			return "", errors.New(HostKeyChanged + ": the Git server's host key changed. Verify it, then trust the new key on the repository.")
		}
		return "", errors.New(cmp.Or(gitError(out), "git failed"))
	}
	_, head := sh(bg, "git", []string{"-C", dir, "log", "-1", "--format=%h %s"}, shOpts{env: env})
	head = strings.TrimSpace(head)
	if len(head) > 200 {
		head = strings.ToValidUTF8(head[:200], "")
	}
	return head, nil
}

// GetRepo loads a repo by id (nil, nil if missing).
func GetRepo(id string) (*Repo, error) { return db.Get[Repo](`SELECT * FROM repos WHERE id = ?`, id) }

// Sync clones or fast-forwards the checkout and returns "<short sha> <subject>". onData may be nil.
// Waits for a running playbook of the same repo; not cancellable (like the JS).
func Sync(repo *Repo, onData func(string)) (string, error) {
	if onData == nil {
		onData = func(string) {}
	}
	unlock, _ := lock(context.Background(), repo.ID)
	defer unlock()
	fresh, err := GetRepo(repo.ID)
	if err != nil {
		return "", err
	}
	return syncUnlocked(cmp.Or(fresh, repo), onData)
}

var notPlaybookDir = regexp.MustCompile(`(^|/)(roles|group_vars|host_vars|vars|defaults|templates|files|tasks|handlers|meta|molecule|\.github|\.gitlab)/`)

// Files lists likely playbooks, syncing first when there is no checkout yet.
func Files(repo *Repo) ([]string, error) {
	dir := repoDir(repo.ID)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if _, err := Sync(repo, nil); err != nil {
			return nil, err
		}
	}
	env := baseEnv()
	env["GIT_CONFIG_NOSYSTEM"] = "1"
	_, out := sh(context.Background(), "git", []string{"-C", dir, "ls-files"}, shOpts{env: env})
	// Likely playbooks first: top level and playbooks/, never vars or role internals.
	fs := []string{}
	for _, f := range strings.Split(out, "\n") {
		if ValidPlaybook(f) && !notPlaybookDir.MatchString(f) {
			fs = append(fs, f)
		}
	}
	// ponytail: case-insensitive order approximates JS localeCompare; use x/text/collate if exact order matters.
	slices.SortStableFunc(fs, func(a, b string) int {
		return cmp.Or(strings.Count(a, "/")-strings.Count(b, "/"), strings.Compare(strings.ToLower(a), strings.ToLower(b)), -strings.Compare(a, b))
	})
	return fs[:min(len(fs), 500)], nil
}

var (
	sshURLHost = regexp.MustCompile(`^ssh://[^@]+@([^:/]+)(?::(\d+))?/`)
	scpURLHost = regexp.MustCompile(`^[\w.-]+@([A-Za-z0-9.-]+):`)
)

// SSHHost maps "git@host:path" / "ssh://user@host:port/path" to its known_hosts name; "" for https.
func SSHHost(url string) string {
	if m := sshURLHost.FindStringSubmatch(url); m != nil {
		if m[2] != "" && m[2] != "22" {
			return "[" + m[1] + "]:" + m[2]
		}
		return m[1]
	}
	if m := scpURLHost.FindStringSubmatch(url); m != nil {
		return m[1]
	}
	return ""
}

// ForgetHostKey removes the pinned Git host key, so the next sync pins the new one. Returns the host.
func ForgetHostKey(repo *Repo) (string, error) {
	host := SSHHost(repo.URL)
	if host == "" {
		return "", httperr.Bad("Only SSH repositories have a host key")
	}
	if _, err := os.Stat(gitKnownHosts()); err == nil {
		sh(context.Background(), "ssh-keygen", []string{"-R", host, "-f", gitKnownHosts()}, shOpts{})
	}
	return host, nil
}

// RemoveCheckout deletes the local clone.
func RemoveCheckout(id string) { _ = os.RemoveAll(repoDir(id)) }

// View is the API shape of a repo: the token never leaves the server.
type View struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	URL        string  `json:"url"`
	Branch     string  `json:"branch"`
	Auth       string  `json:"auth"`
	KeyID      *string `json:"key_id"`
	Username   *string `json:"username"`
	HasToken   bool    `json:"has_token"`
	LastSync   *int64  `json:"last_sync"`
	LastCommit *string `json:"last_commit"`
	LastError  *string `json:"last_error"`
	CreatedAt  int64   `json:"created_at"`
}

func RepoView(r *Repo) View {
	return View{r.ID, r.Name, r.URL, r.Branch, r.Auth, r.KeyID, r.Username, r.TokenEnc != nil, r.LastSync, r.LastCommit, r.LastError, r.CreatedAt}
}

// ---------- ansible ----------

// ToolVersions: first line of `--version`, nil when the tool is missing.
type ToolVersions struct {
	Git     *string `json:"git"`
	Ansible *string `json:"ansible"`
}

var toolCache struct {
	sync.Mutex
	t time.Time
	v ToolVersions
}

func Tools() ToolVersions {
	toolCache.Lock()
	defer toolCache.Unlock()
	if !toolCache.t.IsZero() && time.Since(toolCache.t) < time.Minute {
		return toolCache.v
	}
	ver := func(cmd string) *string {
		code, out := sh(context.Background(), cmd, []string{"--version"}, shOpts{timeout: 20 * time.Second})
		if code != 0 {
			return nil
		}
		l := lastLine(strings.Split(out, "\n")[0])
		return &l
	}
	toolCache.v = ToolVersions{Git: ver("git"), Ansible: ver("ansible-playbook")}
	toolCache.t = time.Now()
	return toolCache.v
}

var (
	recapRE  = regexp.MustCompile(`^(\S+)\s+:\s+ok=(\d+)\s+changed=(\d+)\s+unreachable=(\d+)\s+failed=(\d+)(?:\s+skipped=(\d+))?`)
	headerRE = regexp.MustCompile(`^(TASK|RUNNING HANDLER|PLAY) \[`)
	resultRE = regexp.MustCompile(`^\w[\w ]*: \[([^\]\s]+)`) // "ok: [web1]", "fatal: [web1 -> localhost]: ..."
)

// Recap is one host's line of the PLAY RECAP.
type Recap struct {
	OK          int `json:"ok"`
	Changed     int `json:"changed"`
	Unreachable int `json:"unreachable"`
	Failed      int `json:"failed"`
}

// Splitter splits ansible output per host: each host gets the task headers it took part in plus its result lines.
// Not safe for concurrent use.
type Splitter struct {
	perHost map[string]string
	recap   map[string]Recap
	task    string
	seen    map[string]bool
	buf     string
	inRecap bool
	cont    string // host whose multi-line result ("=> {" ... "}") is still being printed
}

func HostSplitter(aliases []string) *Splitter {
	s := &Splitter{perHost: map[string]string{}, recap: map[string]Recap{}, seen: map[string]bool{}}
	for _, a := range aliases {
		s.perHost[a] = ""
	}
	return s
}

func (s *Splitter) add(a, l string) {
	if len(s.perHost[a]) < 500_000 {
		s.perHost[a] += l + "\n"
	}
}

func (s *Splitter) line(l string) {
	if s.cont != "" {
		s.add(s.cont, l)
		if l == "}" {
			s.cont = ""
		}
		return
	}
	if headerRE.MatchString(l) {
		s.task, s.seen, s.inRecap = l, map[string]bool{}, false
		return
	}
	if strings.HasPrefix(l, "PLAY RECAP") {
		s.inRecap = true
		return
	}
	if s.inRecap {
		if m := recapRE.FindStringSubmatch(l); m != nil {
			if _, ok := s.perHost[m[1]]; ok {
				n := func(i int) int { v, _ := strconv.Atoi(m[i]); return v }
				s.recap[m[1]] = Recap{n(2), n(3), n(4), n(5)}
			}
		}
		return
	}
	if m := resultRE.FindStringSubmatch(l); m != nil {
		a := m[1]
		if _, ok := s.perHost[a]; !ok {
			return
		}
		if !s.seen[a] {
			s.add(a, s.task)
			s.seen[a] = true
		}
		s.add(a, l)
		if strings.HasSuffix(l, "=> {") {
			s.cont = a
		}
	}
}

func (s *Splitter) Write(d string) {
	s.buf += d
	for {
		i := strings.IndexByte(s.buf, '\n')
		if i < 0 {
			return
		}
		s.line(s.buf[:i])
		s.buf = s.buf[i+1:]
	}
}

func (s *Splitter) End() (perHost map[string]string, recap map[string]Recap) {
	if s.buf != "" {
		s.line(s.buf)
		s.buf = ""
	}
	return s.perHost, s.recap
}

var (
	badChar  = regexp.MustCompile(`[^A-Za-z0-9_.-]`)
	badGroup = regexp.MustCompile(`[^A-Za-z0-9_]`)
)

type PlaybookOpts struct {
	RepoID, Playbook string
	Servers          []db.Server
	Become           bool
	Vars             map[string]any // extra vars; checkbox parameters are real booleans
	Check            bool
	OnData           func(string)
}

type PlaybookResult struct {
	Code    int
	Commit  string
	Alias   map[string]string // server id -> inventory host name
	PerHost map[string]string // alias -> output
	Recap   map[string]Recap  // alias -> recap
}

// RunPlaybook runs a playbook against Fleetdeck servers. Cancelling ctx stops waiting for the repo lock and
// kills ansible-galaxy/ansible-playbook (their whole process group).
func RunPlaybook(ctx context.Context, o PlaybookOpts) (*PlaybookResult, error) {
	unlock, err := lock(ctx, o.RepoID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	repo, err := GetRepo(o.RepoID)
	if err != nil {
		return nil, err
	}
	if repo == nil {
		return nil, errors.New("The repository of this script was deleted")
	}
	if !ValidPlaybook(o.Playbook) {
		return nil, errors.New("Invalid playbook path")
	}
	commit, err := syncUnlocked(repo, o.OnData)
	if err != nil {
		return nil, err
	}
	o.OnData("At commit " + commit + "\n\n")
	dir := repoDir(o.RepoID)
	if _, err := os.Stat(filepath.Join(dir, o.Playbook)); err != nil {
		return nil, fmt.Errorf("Playbook %s does not exist on branch %s", o.Playbook, repo.Branch)
	}

	tmp, err := tempDir("ansible-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	extra := filepath.Join(dir, ".fleetdeck")
	env := baseEnv()
	for k, v := range map[string]string{
		"PYTHONUNBUFFERED": "1", "ANSIBLE_NOCOLOR": "1", "ANSIBLE_FORCE_COLOR": "0", "ANSIBLE_RETRY_FILES_ENABLED": "False",
		"ANSIBLE_HOST_KEY_CHECKING": "True", "ANSIBLE_LOCAL_TEMP": filepath.Join(tmp, "tmp"),
		// Same host-key pinning as the rest of Fleetdeck: only the pinned keys are trusted.
		"ANSIBLE_SSH_COMMON_ARGS":  "-o StrictHostKeyChecking=yes -o UserKnownHostsFile=" + filepath.Join(tmp, "known_hosts"),
		"ANSIBLE_COLLECTIONS_PATH": strings.Join([]string{filepath.Join(extra, "collections"), "~/.ansible/collections", "/usr/share/ansible/collections"}, ":"),
		"ANSIBLE_ROLES_PATH":       strings.Join([]string{filepath.Join(dir, "roles"), filepath.Join(extra, "roles"), "~/.ansible/roles", "/usr/share/ansible/roles", "/etc/ansible/roles"}, ":"),
	} {
		env[k] = v
	}
	for _, g := range [][2]string{{"collection", "collections/requirements.yml"}, {"role", "roles/requirements.yml"}} {
		kind, file := g[0], g[1]
		if _, err := os.Stat(filepath.Join(dir, file)); err != nil {
			continue
		}
		o.OnData(fmt.Sprintf("$ ansible-galaxy %s install -r %s\n", kind, file))
		if code, _ := sh(ctx, "ansible-galaxy", []string{kind, "install", "-r", file, "-p", filepath.Join(extra, kind+"s")}, shOpts{dir: dir, env: env, onData: o.OnData}); code != 0 {
			return nil, fmt.Errorf("ansible-galaxy %s install failed", kind)
		}
	}

	alias := map[string]string{}
	used := map[string]bool{}
	hosts := map[string]map[string]any{}
	groups := map[string]map[string]map[string]struct{}{}
	var known []string
	for i := range o.Servers {
		s := &o.Servers[i]
		if s.HostKeyRaw == nil || *s.HostKeyRaw == "" {
			o.OnData(fmt.Sprintf("! Skipping %s: its host key is not pinned yet. Open the server once so Fleetdeck can connect.\n", s.Name))
			continue
		}
		a := badChar.ReplaceAllString(s.Name, "_")
		for used[a] {
			a += "_"
		}
		used[a] = true
		alias[s.ID] = a
		h := s.Host
		if s.Port != 22 {
			h = fmt.Sprintf("[%s]:%d", s.Host, s.Port)
		}
		known = append(known, h+" "+*s.HostKeyRaw)
		hosts[a] = map[string]any{"ansible_host": s.Host, "ansible_port": s.Port, "ansible_user": s.User}
		if s.KeyID != nil && *s.KeyID != "" {
			f, err := writeKey(tmp, *s.KeyID)
			if err != nil {
				return nil, err
			}
			hosts[a]["ansible_ssh_private_key_file"] = f
		}
		// Lives only in the 0600 inventory inside the private temp dir, deleted after the run.
		if o.Become && ssh.SudoUsable(s) {
			pw, err := sec.Decrypt(db.Str(s.SudoEnc))
			if err != nil {
				return nil, err
			}
			hosts[a]["ansible_become_password"] = pw
		}
		for _, t := range strings.Split(s.Tags, ",") {
			if t == "" {
				continue
			}
			g := badGroup.ReplaceAllString(t, "_")
			if groups[g] == nil {
				groups[g] = map[string]map[string]struct{}{"hosts": {}}
			}
			groups[g]["hosts"][a] = struct{}{}
		}
	}
	if len(alias) == 0 {
		return nil, errors.New("None of the target servers can be used")
	}
	// JSON is valid YAML, so the yaml inventory plugin reads it; no INI quoting pitfalls.
	inv, _ := json.Marshal(map[string]any{"all": map[string]any{"hosts": hosts, "children": groups, "vars": map[string]bool{"ansible_become": o.Become}}})
	vars := o.Vars
	if vars == nil {
		vars = map[string]any{}
	}
	varsJSON, _ := json.Marshal(vars)
	for name, data := range map[string][]byte{"known_hosts": []byte(strings.Join(known, "\n") + "\n"), "inventory.yml": inv, "vars.json": varsJSON} {
		if err := os.WriteFile(filepath.Join(tmp, name), data, 0o600); err != nil {
			return nil, err
		}
	}

	args := []string{"-i", filepath.Join(tmp, "inventory.yml"), "-e", "@" + filepath.Join(tmp, "vars.json")}
	check := ""
	if o.Check {
		args = append(args, "--check", "--diff")
		check = " --check --diff"
	}
	args = append(args, o.Playbook)
	plural := "s"
	if len(alias) == 1 {
		plural = ""
	}
	o.OnData(fmt.Sprintf("$ ansible-playbook%s %s  (%d host%s)\n", check, o.Playbook, len(alias), plural))
	aliases := make([]string, 0, len(alias))
	for _, a := range alias {
		aliases = append(aliases, a)
	}
	split := HostSplitter(aliases)
	code, _ := sh(ctx, "ansible-playbook", args, shOpts{dir: dir, env: env, onData: func(d string) { split.Write(d); o.OnData(d) }})
	perHost, recap := split.End()
	return &PlaybookResult{Code: code, Commit: commit, Alias: alias, PerHost: perHost, Recap: recap}, nil
}
