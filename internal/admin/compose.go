package admin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"fleetdeck/internal/db"
	"fleetdeck/internal/httperr"
)

// ---- compose files ----
// Which files may be read and written comes from the stack's own container labels, never from the request.

var (
	projectRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)
	hashRe    = regexp.MustCompile(`^[a-f0-9]{64}$`)
	tmpRe     = regexp.MustCompile(`\.fleetdeck\.[A-Za-z0-9]{6}`)
)

const composeMax = 1 << 20

type ComposeFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Hash    string `json:"hash"` // sha256 of Content as read; a save names it so it can't overwrite someone else's edit
	Env     bool   `json:"env"`  // an environment file (.env): values can be secrets
	Missing bool   `json:"missing,omitempty"`
}

type Compose struct {
	Project string        `json:"project"`
	Dir     string        `json:"dir"`
	Files   []ComposeFile `json:"files"`
}

type stack struct {
	dir         string
	files, envs []string
}

func (st stack) allowed(p string) bool {
	return slices.Contains(st.files, p) || slices.Contains(st.envs, p)
}

func findStack(ctx context.Context, s *db.Server, project string) (stack, error) {
	if !projectRe.MatchString(project) {
		return stack{}, httperr.Bad("invalid project name")
	}
	out, err := sh(ctx, s, stackScript(project), shOpts{})
	if err != nil {
		return stack{}, err
	}
	return parseStack(out)
}

func stackScript(project string) string {
	return `docker ps -a --filter ` + q("label=com.docker.compose.project="+project) + ` --format '{{.Label "com.docker.compose.project.working_dir"}}	{{.Label "com.docker.compose.project.config_files"}}	{{.Label "com.docker.compose.project.environment_file"}}'`
}

func parseStack(out string) (stack, error) {
	var st stack
	add := func(list *[]string, csv string) {
		for _, f := range strings.Split(csv, ",") {
			if f = strings.TrimSpace(f); f != "" {
				if !path.IsAbs(f) {
					f = path.Join(st.dir, f)
				}
				if f = path.Clean(f); !slices.Contains(*list, f) {
					*list = append(*list, f)
				}
			}
		}
	}
	for _, l := range strings.Split(out, "\n") {
		p := strings.Split(l, "\t")
		if len(p) < 2 || !path.IsAbs(p[0]) || (st.dir != "" && path.Clean(p[0]) != st.dir) {
			continue
		}
		st.dir = path.Clean(p[0])
		add(&st.files, p[1])
		add(&st.envs, at(p, 2))
	}
	if st.dir == "" || len(st.files) == 0 {
		return st, httperr.New(404, "The stack's containers don't say where its Compose files are")
	}
	if len(st.envs) == 0 {
		st.envs = []string{path.Join(st.dir, ".env")} // what Compose reads when no --env-file was given
	}
	return st, nil
}

// ReadCompose returns a stack's compose and environment files. Environment files that don't exist are left out.
func ReadCompose(ctx context.Context, s *db.Server, project string) (*Compose, error) {
	st, err := findStack(ctx, s, project)
	if err != nil {
		return nil, err
	}
	out, err := sh(ctx, s, readScript(st), shOpts{maxOutput: 8 * composeMax})
	if err != nil {
		return nil, err
	}
	return parseRead(project, st, out)
}

func readScript(st stack) string {
	script := ""
	for _, f := range append(slices.Clone(st.files), st.envs...) {
		script += fmt.Sprintf(`echo '#file'
if [ ! -f %[1]s ]; then echo '#missing'
elif [ "$(wc -c < %[1]s)" -gt %[2]d ]; then echo '#big'
else base64 < %[1]s | tr -d '\n'; echo; fi
`, q(f), composeMax)
	}
	return script
}

func parseRead(project string, st stack, out string) (*Compose, error) {
	all := append(slices.Clone(st.files), st.envs...)
	c := &Compose{Project: project, Dir: st.dir, Files: []ComposeFile{}}
	for i, part := range strings.Split(strings.TrimPrefix(out, "#file\n"), "#file\n") {
		if i >= len(all) {
			break
		}
		f := ComposeFile{Path: all[i], Env: i >= len(st.files)}
		switch body := strings.TrimSpace(part); body {
		case "#missing":
			if f.Env {
				continue
			}
			f.Missing = true
		case "#big":
			return nil, httperr.Bad(f.Path + " is larger than 1 MB, too big to edit here")
		default:
			b, err := base64.StdEncoding.DecodeString(body)
			if err != nil || !utf8.Valid(b) {
				return nil, httperr.Bad(f.Path + " isn't a text file")
			}
			sum := sha256.Sum256(b)
			f.Content, f.Hash = string(b), hex.EncodeToString(sum[:])
		}
		c.Files = append(c.Files, f)
	}
	return c, nil
}

type ComposeEdit struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Hash    string `json:"hash"`
}

// SaveCompose checks the edited files with `docker compose config` and only then writes them, keeping a .bak of
// each. A file that changed on the server since it was read is refused.
func SaveCompose(ctx context.Context, s *db.Server, project string, edits []ComposeEdit) error {
	st, err := findStack(ctx, s, project)
	if err != nil {
		return err
	}
	script, err := saveScript(project, st, edits)
	if err != nil {
		return err
	}
	out, err := sh(ctx, s, script, shOpts{timeout: 90 * time.Second, allowFail: true})
	if err != nil {
		return err
	}
	return saveResult(out)
}

// saveScript writes each edit to a temp file next to its original, checks the project with them in place of the
// originals, then copies them over (in place, so owner and mode stay).
func saveScript(project string, st stack, edits []ComposeEdit) (string, error) {
	if len(edits) == 0 || len(edits) > 20 {
		return "", httperr.Bad("save 1 to 20 files at a time")
	}
	tmp := map[string]string{} // path -> shell variable of its temp file
	vars := ""
	for i := range edits {
		vars += fmt.Sprintf(` "$t%d"`, i)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `cd -- %s || exit 0
if docker compose version >/dev/null 2>&1; then dc="docker compose"
elif command -v docker-compose >/dev/null 2>&1; then dc=docker-compose
else echo '#err Docker Compose is not installed'; exit 0; fi
trap 'rm -f%s' EXIT
`, q(st.dir), vars)
	for i, e := range edits {
		switch {
		case !st.allowed(e.Path):
			return "", httperr.Bad(e.Path + " isn't one of this stack's files")
		case tmp[e.Path] != "":
			return "", httperr.Bad(e.Path + " is in the request twice")
		case !hashRe.MatchString(e.Hash):
			return "", httperr.Bad("invalid hash")
		case len(e.Content) > composeMax || !utf8.ValidString(e.Content) || strings.ContainsRune(e.Content, 0):
			return "", httperr.Bad(e.Path + " must be text of at most 1 MB")
		}
		v := fmt.Sprintf("t%d", i)
		tmp[e.Path] = v
		fmt.Fprintf(&b, `[ "$(sha256sum < %[1]s | cut -d' ' -f1)" = %[2]s ] || { echo '#conflict '%[1]s; exit 0; }
%[3]s=$(mktemp %[4]s) || { echo '#err Can'\''t write next to '%[1]s; exit 0; }
base64 -d > "$%[3]s" <<'FLEETDECK_EOF'
%[5]s
FLEETDECK_EOF
`, q(e.Path), q(e.Hash), v, q(e.Path+".fleetdeck.XXXXXX"), wrap64(e.Content))
	}
	arg := func(p string) string {
		if v := tmp[p]; v != "" {
			return `"$` + v + `"`
		}
		return q(p)
	}
	fmt.Fprintf(&b, "set -- -p %s --project-directory %s\n", q(project), q(st.dir))
	for _, e := range st.envs {
		fmt.Fprintf(&b, "[ -f %s ] && set -- \"$@\" --env-file %s\n", arg(e), arg(e))
	}
	for _, f := range st.files {
		fmt.Fprintf(&b, "set -- \"$@\" -f %s\n", arg(f))
	}
	b.WriteString(`if ! msg=$($dc "$@" config -q 2>&1); then echo '#invalid'; printf '%s\n' "$msg"; exit 0; fi
`)
	for _, e := range edits {
		fmt.Fprintf(&b, "cp -p -- %[1]s %[2]s && cat -- \"$%[3]s\" > %[1]s || { echo '#err Couldn'\\''t write '%[1]s; exit 0; }\n", q(e.Path), q(e.Path+".bak"), tmp[e.Path])
	}
	b.WriteString("echo '#saved'\n")
	return b.String(), nil
}

func wrap64(s string) string {
	e := base64.StdEncoding.EncodeToString([]byte(s))
	var lines []string
	for len(e) > 76 {
		lines, e = append(lines, e[:76]), e[76:]
	}
	return strings.Join(append(lines, e), "\n")
}

func saveResult(out string) error {
	out = strings.TrimSpace(out)
	switch {
	case strings.HasSuffix(out, "#saved"):
		return nil
	case strings.Contains(out, "#conflict "):
		_, f, _ := strings.Cut(out, "#conflict ")
		return httperr.New(409, strings.TrimSpace(f)+" changed on the server since you opened it. Close the editor and open it again to see the new version.")
	case strings.Contains(out, "#invalid"):
		_, msg, _ := strings.Cut(out, "#invalid")
		return httperr.Bad("Compose rejects the change, nothing was saved: " + strings.TrimSpace(tmpRe.ReplaceAllString(msg, "")))
	case strings.Contains(out, "#err "):
		_, msg, _ := strings.Cut(out, "#err ")
		return httperr.Bad(strings.TrimSpace(msg))
	}
	lines := strings.Split(out, "\n")
	return httperr.New(502, "Saving failed: "+strings.Join(lines[max(0, len(lines)-3):], " "))
}
