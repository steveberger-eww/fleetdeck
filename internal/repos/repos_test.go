package repos

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"fleetdeck/internal/db"
	"fleetdeck/internal/sec"
)

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "fleetdeck-test-")
	if err := db.OpenTest(dir); err != nil {
		panic(err)
	}
	if err := sec.Init(); err != nil {
		panic(err)
	}
	if err := Init(); err != nil {
		panic(err)
	}
	code := m.Run()
	db.DropTest()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestRepositoryValidation(t *testing.T) {
	ok := func(b RepoInput) (*Repo, error) { b.Name = "infra"; return ValidateRepo("default", b, nil) }
	if r, err := ok(RepoInput{URL: "https://github.com/org/infra.git"}); err != nil || r.Branch != "main" {
		t.Fatal(r, err)
	}
	for _, b := range []RepoInput{{URL: "git@github.com:org/infra.git", Branch: "release/1.2"}, {URL: "ssh://git@git.example.com:2222/org/infra.git"}} {
		if _, err := ok(b); err != nil {
			t.Fatal(b.URL, err)
		}
	}
	for _, url := range []string{"--upload-pack=touch /tmp/x", "ext::sh -c id", "file:///etc", "/srv/repo", "http://plain.example/repo.git", "https://x/ y", "https://x/a b"} {
		if _, err := ok(RepoInput{URL: url}); err == nil {
			t.Error(url)
		}
	}
	for _, b := range []RepoInput{
		{URL: "https://github.com/o/r.git", Branch: "--orphan"},
		{URL: "https://github.com/o/r.git", Branch: "a/../b"},
		{URL: "git@github.com:o/r.git", Auth: "token", Token: "x"}, // token needs https
	} {
		if _, err := ok(b); err == nil {
			t.Error(b)
		}
	}
	r, err := ok(RepoInput{URL: "https://github.com/o/r.git", Auth: "token", Username: "x-access-token", Token: "secret"})
	if err != nil || !strings.HasPrefix(*r.TokenEnc, "v1:") || strings.Contains(*r.TokenEnc, "secret") {
		t.Fatal(r, err)
	}
}

func TestPlaybookPathsStayInsideTheRepo(t *testing.T) {
	for _, p := range []string{"site.yml", "playbooks/web.yaml", "a-b_c/d.yml"} {
		if !ValidPlaybook(p) {
			t.Error(p)
		}
	}
	for _, p := range []string{"../etc/x.yml", "a/../../x.yml", "/etc/x.yml", "-x.yml", "site.sh", "a b.yml"} {
		if ValidPlaybook(p) {
			t.Error(p)
		}
	}
}

func TestAnsibleOutputSplitPerHostWithRecap(t *testing.T) {
	sp := HostSplitter([]string{"web1", "db_1"})
	sp.Write("PLAY [all] ***\n\nTASK [Gathering Facts] ***\nok: [web1]\nfatal: [db_1]: UNREACHABLE! => {\"msg\": \"x\"}\n")
	sp.Write("\nTASK [ping] ***\nchanged: [web1] => (item=a)\nok: [web1] => {\n    \"msg\": \"hello\"\n}\n\nPLAY RECAP ***\n")
	sp.Write("web1                       : ok=2    changed=1    unreachable=0    failed=0    skipped=0\ndb_1   : ok=0    changed=0    unreachable=1    failed=0\n")
	perHost, recap := sp.End()
	if recap["web1"] != (Recap{OK: 2, Changed: 1}) {
		t.Error(recap["web1"])
	}
	if recap["db_1"].Unreachable != 1 {
		t.Error(recap["db_1"])
	}
	web := perHost["web1"]
	if !regexp.MustCompile(`TASK \[ping\] \*\*\*\nchanged: \[web1\]`).MatchString(web) || strings.Contains(web, "db_1") ||
		!regexp.MustCompile(`"msg": "hello"\n}`).MatchString(web) {
		t.Error(web)
	}
}

func TestGitHostExtractionForKnownHosts(t *testing.T) {
	for url, want := range map[string]string{
		"git@github.com:org/r.git":                 "github.com",
		"ssh://git@git.example.com:2222/org/r.git": "[git.example.com]:2222",
		"ssh://git@git.example.com/org/r.git":      "git.example.com",
		"https://github.com/org/r.git":             "", // JS: null
	} {
		if got := SSHHost(url); got != want {
			t.Errorf("%s: %q", url, got)
		}
	}
}

// Not in test.js: the child-process guarantees (no secret in the env, ctx kills the group, missing tools).
func TestShIsolatedAndCancellable(t *testing.T) {
	t.Setenv("FLEETDECK_SECRET", "leak")
	if code, out := sh(context.Background(), "sh", []string{"-c", "echo ${FLEETDECK_SECRET:-none}"}, shOpts{}); code != 0 || out != "none\n" {
		t.Fatal(code, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if code, _ := sh(ctx, "sh", []string{"-c", "sleep 30; echo done"}, shOpts{}); code == 0 || time.Since(start) > 5*time.Second {
		t.Fatal("not killed", code)
	}
	if code, out := sh(context.Background(), "sh", []string{"-c", "sleep 30"}, shOpts{timeout: 100 * time.Millisecond}); code == 0 || !strings.Contains(out, "[timed out after") {
		t.Fatal(code, out)
	}
	if code, out := sh(context.Background(), "no-such-tool-xyz", nil, shOpts{}); code != 127 || out != "no-such-tool-xyz is not installed on the Fleetdeck host" {
		t.Fatal(code, out)
	}
}
