// Package registry asks image registries, anonymously, which tags a repository has and which image a tag
// points at now. Answers are cached for hours: Docker Hub limits anonymous clients.
package registry

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"fleetdeck/internal/checks"
)

// Ref is an image reference split the way Docker reads it. Registry is the API host.
type Ref struct{ Registry, Repo, Tag, Digest string }

var (
	hostRe   = regexp.MustCompile(`^[A-Za-z0-9.-]+(:[0-9]{1,5})?$`)
	repoRe   = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)
	tagRe    = regexp.MustCompile(`^[\w][\w.-]{0,127}$`)
	digestRe = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// Parse reads "nginx", "user/app:1.2", "ghcr.io/org/app:v2@sha256:…". ok is false for image ids and junk.
func Parse(s string) (r Ref, ok bool) {
	if strings.HasPrefix(s, "sha256:") {
		return r, false // an image id: the container wasn't started from a name
	}
	name, digest, _ := strings.Cut(s, "@")
	if digest != "" && !digestRe.MatchString(digest) {
		return r, false
	}
	r.Digest = digest
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name, r.Tag = name[:i], name[i+1:]
	}
	first, rest, found := strings.Cut(name, "/")
	if found && (strings.ContainsAny(first, ".:") || first == "localhost") {
		r.Registry, name = first, rest
	}
	switch r.Registry {
	case "", "docker.io", "index.docker.io":
		r.Registry = "registry-1.docker.io"
		if !strings.Contains(name, "/") {
			name = "library/" + name
		}
	}
	if r.Tag == "" && r.Digest == "" {
		r.Tag = "latest"
	}
	r.Repo = name
	return r, hostRe.MatchString(r.Registry) && repoRe.MatchString(r.Repo) && (r.Tag == "" || tagRe.MatchString(r.Tag))
}

// ErrAuth: the registry wants a login. Fleetdeck holds no registry credentials.
var ErrAuth = errors.New("the registry needs a login, so only public images can be checked")

// ErrNotFound: no such repository or tag (private, built on the server, or removed upstream).
var ErrNotFound = errors.New("not found in the registry")

// Same address rules as endpoint checks: no loopback or link-local (cloud metadata) targets.
var client = &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{DialContext: checks.Dialer().DialContext, Proxy: http.ProxyFromEnvironment}}

const (
	ttl      = 6 * time.Hour
	errTTL   = 30 * time.Minute
	maxPages = 50
	pageCap  = 8 << 20
)

type cached struct {
	at   time.Time
	tags []string
	dig  string
	err  error
}

// ponytail: in-memory and never pruned; one entry per repository and tag seen, lost on restart.
var (
	mu    sync.Mutex
	cache = map[string]cached{}
)

func lookup(key string, refresh bool, fetch func() cached) cached {
	mu.Lock()
	c, ok := cache[key]
	mu.Unlock()
	keep := ttl
	if c.err != nil {
		keep = errTTL
	}
	if ok && !refresh && time.Since(c.at) < keep {
		return c
	}
	c = fetch()
	c.at = time.Now()
	mu.Lock()
	cache[key] = c
	mu.Unlock()
	return c
}

// session talks to one repository and keeps its pull token.
type session struct {
	ctx   context.Context
	ref   Ref
	token string
}

var challengeRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

// do sends a request, fetching an anonymous pull token when the registry asks for one (the Docker token flow).
func (s *session) do(method, u, accept string) (*http.Response, error) {
	resp, err := s.send(method, u, accept)
	if err == nil && resp.StatusCode == 401 && s.token == "" {
		resp.Body.Close()
		if err := s.fetchToken(resp.Header.Get("WWW-Authenticate")); err != nil {
			return nil, err
		}
		resp, err = s.send(method, u, accept)
	}
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		resp.Body.Close()
		switch resp.StatusCode {
		case 401, 403:
			return nil, ErrAuth
		case 404:
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("the registry answered HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

func (s *session) send(method, u, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(s.ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Fleetdeck image update check")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	return client.Do(req)
}

func (s *session) fetchToken(challenge string) error {
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return ErrAuth // Basic auth: a login is the only way in
	}
	p := map[string]string{}
	for _, m := range challengeRe.FindAllStringSubmatch(challenge, -1) {
		p[strings.ToLower(m[1])] = m[2]
	}
	realm, err := url.Parse(p["realm"])
	if err != nil || (realm.Scheme != "https" && realm.Scheme != "http") || realm.Host == "" {
		return ErrAuth
	}
	q := realm.Query()
	if p["service"] != "" {
		q.Set("service", p["service"])
	}
	q.Set("scope", "repository:"+s.ref.Repo+":pull")
	realm.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(s.ctx, "GET", realm.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Fleetdeck image update check")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ErrAuth
	}
	var raw struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&raw) != nil {
		return ErrAuth
	}
	s.token = cmp.Or(raw.Token, raw.AccessToken)
	if s.token == "" {
		return ErrAuth
	}
	return nil
}

func base(r Ref) string { return "https://" + r.Registry + "/v2/" + r.Repo }

var nextRe = regexp.MustCompile(`<([^>]+)>\s*;\s*rel="?next"?`)

func (s *session) tags() ([]string, error) {
	var all []string
	u := base(s.ref) + "/tags/list?n=1000"
	for range maxPages {
		resp, err := s.do("GET", u, "application/json")
		if err != nil {
			return nil, err
		}
		var page struct{ Tags []string }
		err = json.NewDecoder(io.LimitReader(resp.Body, pageCap)).Decode(&page)
		link := resp.Header.Get("Link")
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("unreadable tag list: %w", err)
		}
		all = append(all, page.Tags...)
		m := nextRe.FindStringSubmatch(link)
		if m == nil {
			return all, nil
		}
		next, err := resp.Request.URL.Parse(m[1])
		if err != nil || next.Host != resp.Request.URL.Host {
			return all, nil
		}
		u = next.String()
	}
	return all, nil // ponytail: stops after maxPages pages; enough for every public image seen so far
}

// Multi-arch indexes first: their digest is what `docker pull` records in RepoDigests.
const manifestTypes = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json"

func (s *session) digest(tag string) (string, error) {
	u := base(s.ref) + "/manifests/" + tag
	resp, err := s.do("HEAD", u, manifestTypes) // HEAD doesn't count against Docker Hub's pull limit
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	if d := resp.Header.Get("Docker-Content-Digest"); digestRe.MatchString(d) {
		return d, nil
	}
	if resp, err = s.do("GET", u, manifestTypes); err != nil {
		return "", err
	}
	defer resp.Body.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(resp.Body, pageCap)); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// Info is what the registry says about one reference: all tags of its repository, and the digest its tag points at
// ("" for references pinned by digest only).
type Info struct {
	Tags   []string
	Digest string
}

// Lookup asks the registry (or the cache; refresh skips it) about r.
func Lookup(ctx context.Context, r Ref, refresh bool) (Info, error) {
	s := &session{ctx: ctx, ref: r}
	t := lookup(r.Registry+"/"+r.Repo, refresh, func() cached { tags, err := s.tags(); return cached{tags: tags, err: err} })
	if t.err != nil {
		return Info{}, t.err
	}
	info := Info{Tags: t.tags}
	if r.Tag != "" {
		d := lookup(r.Registry+"/"+r.Repo+":"+r.Tag, refresh, func() cached { d, err := s.digest(r.Tag); return cached{dig: d, err: err} })
		if d.err != nil && !errors.Is(d.err, ErrNotFound) {
			return Info{}, d.err
		}
		info.Digest = d.dig
	}
	return info, nil
}
