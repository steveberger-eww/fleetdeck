package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"fleetdeck/internal/registry"
)

func TestParseStack(t *testing.T) {
	st, err := parseStack("/srv/app\t/srv/app/compose.yml,override.yml\t\n/srv/app\t/srv/app/compose.yml\t\n/elsewhere\t/x.yml\t\n")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, st, stack{"/srv/app", []string{"/srv/app/compose.yml", "/srv/app/override.yml"}, []string{"/srv/app/.env"}})
	st, _ = parseStack("/srv/app\t/srv/app/compose.yml\t/srv/app/prod.env\n")
	eq(t, st.envs, []string{"/srv/app/prod.env"})
	if _, err := parseStack("\t\t\n"); err == nil {
		t.Fatal("no directory should fail")
	}
}

// The save script against a fake docker: refuses stale hashes and files Compose rejects, else writes and keeps .bak.
func TestSaveScript(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	os.Mkdir(bin, 0o755)
	// "docker compose … config -q": fails when any -f file says "broken", naming that file like Compose would.
	os.WriteFile(filepath.Join(bin, "docker"), []byte(`#!/bin/sh
[ "$2" = version ] && exit 0
while [ $# -gt 0 ]; do [ "$1" = -f ] && grep -q broken "$2" && { echo "yaml: invalid in $2"; exit 1; }; shift; done
exit 0
`), 0o755)
	file := filepath.Join(dir, "compose.yml")
	os.WriteFile(file, []byte("services: {}\n"), 0o640)
	sum := sha256.Sum256([]byte("services: {}\n"))
	hash := hex.EncodeToString(sum[:])
	st := stack{dir, []string{file}, []string{filepath.Join(dir, ".env")}}
	run := func(edits ...ComposeEdit) error {
		script, err := saveScript("app", st, edits)
		if err != nil {
			return err
		}
		cmd := exec.Command("sh", "-s")
		cmd.Stdin = strings.NewReader(script)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		out, _ := cmd.CombinedOutput()
		return saveResult(string(out))
	}

	err := run(ComposeEdit{file, "services: broken\n", hash})
	if err == nil || !strings.Contains(err.Error(), "yaml: invalid in "+file+"\n") && !strings.HasSuffix(err.Error(), "yaml: invalid in "+file) {
		t.Fatalf("got %v", err)
	}
	if err := run(ComposeEdit{file, "services: {web: {}}\n", strings.Repeat("0", 64)}); err == nil || !strings.Contains(err.Error(), "changed on the server") {
		t.Fatalf("got %v", err)
	}
	if err := run(ComposeEdit{"/etc/passwd", "x", hash}); err == nil {
		t.Fatal("a file outside the stack was accepted")
	}
	if err := run(ComposeEdit{file, "services: {web: {}}\nquote: \"it's\"\n", hash}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(file)
	eq(t, string(b), "services: {web: {}}\nquote: \"it's\"\n")
	b, _ = os.ReadFile(file + ".bak")
	eq(t, string(b), "services: {}\n")
	fi, _ := os.Stat(file)
	eq(t, fi.Mode().Perm(), os.FileMode(0o640))
	left, _ := filepath.Glob(filepath.Join(dir, "*.fleetdeck.*"))
	eq(t, len(left), 0)
}

func TestImageJudge(t *testing.T) {
	ctrs, images, refs := parseImages(`"c1" "nginx:1.25.3" "sha256:i1"
"c2" "redis:latest" "sha256:i2"
"c3" "sha256:i3" "sha256:i3"
#images
"sha256:i1" ["nginx@sha256:d1"] {"Env":["PATH=/bin"]}
"sha256:i2" ["redis@sha256:d2"] {"Labels":{"org.opencontainers.image.version":"7.2.4"}}
#refs
nginx:1.25.3	sha256:i1
redis:latest	sha256:i9
`)
	eq(t, len(ctrs), 3)
	eq(t, images["sha256:i2"].version, "7.2.4")
	tags, redis := []string{"1.25.3", "1.25.4", "1.27.0", "2.0.0"}, []string{"latest", "7.2.4", "7.4.1"}

	u := judge(ctrs[0], images[ctrs[0].image], refs[ctrs[0].ref], &answer{info: registry.Info{Tags: tags, Digest: "sha256:d1"}})
	eq(t, [4]any{u.Step, u.Newest, u.Safe, u.NewBuild}, [4]any{registry.Major, "2.0.0", "1.27.0", false})

	// latest: a new image behind the tag, compared by its version label; and a newer one already pulled.
	u = judge(ctrs[1], images[ctrs[1].image], refs[ctrs[1].ref], &answer{info: registry.Info{Tags: redis, Digest: "sha256:d3"}})
	eq(t, [5]any{u.Step, u.Newest, u.Channel, u.NewBuild, u.Pulled}, [5]any{registry.Minor, "7.4.1", true, true, true})
	u = judge(ctrs[1], images[ctrs[1].image], "", &answer{info: registry.Info{Tags: redis, Digest: "sha256:d2"}})
	eq(t, [2]any{u.Step, u.Newest}, [2]any{registry.Current, ""})

	u = judge(ctrs[2], images[ctrs[2].image], "", nil)
	eq(t, u.Step, "")
	if u.Error == "" {
		t.Fatal("an image id has nothing to compare")
	}
}
