package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func eq(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestParse(t *testing.T) {
	for in, want := range map[string]Ref{
		"nginx":                                {"registry-1.docker.io", "library/nginx", "latest", ""},
		"user/app:1.2":                         {"registry-1.docker.io", "user/app", "1.2", ""},
		"docker.io/library/redis:7":            {"registry-1.docker.io", "library/redis", "7", ""},
		"ghcr.io/org/app:v2.0.1-alpine":        {"ghcr.io", "org/app", "v2.0.1-alpine", ""},
		"reg.lan:5000/team/svc":                {"reg.lan:5000", "team/svc", "latest", ""},
		"localhost/x:dev":                      {"localhost", "x", "dev", ""},
		"pg@sha256:" + strings.Repeat("a", 64): {"registry-1.docker.io", "library/pg", "", "sha256:" + strings.Repeat("a", 64)},
	} {
		r, ok := Parse(in)
		if !ok {
			t.Fatalf("%s: not parsed", in)
		}
		eq(t, r, want)
	}
	for _, bad := range []string{"sha256:" + strings.Repeat("a", 64), "Upper/Case", "a@sha256:short", ""} {
		if _, ok := Parse(bad); ok {
			t.Fatalf("%q parsed", bad)
		}
	}
}

func TestCompare(t *testing.T) {
	tags := []string{"latest", "1.24.0", "1.25.3", "1.25.4", "1.27.2", "2.0.0", "2.1.0", "3.0.0-rc1", "3.0.0-alpine",
		"1.25.3-alpine", "1.25.5-alpine", "1.27", "2.1", "20231012", "16", "17", "18-bookworm"}
	eq(t, Compare("1.25.3", tags), Compared{Major, "2.1.0", "1.27.2", 1})
	eq(t, Compare("2.1.0", tags), Compared{})                                                  // the rc and the alpine flavor don't count
	eq(t, Compare("1.25.3-alpine", tags), Compared{Major, "3.0.0-alpine", "1.25.5-alpine", 1}) // only its own flavor
	eq(t, Compare("1.25", tags), Compared{Major, "2.1", "1.27", 1})                            // same number of parts
	eq(t, Compare("16", tags), Compared{Step: Major, Newest: "17", Behind: 1})                 // 20231012 is a date, not version 20 million
	eq(t, Compare("v1.27.2", []string{"v1.27.2", "v1.28.0"}), Compared{Step: Minor, Newest: "v1.28.0"})
	eq(t, Compare("4.0.11-ls250", []string{"4.0.11-ls251", "4.0.11-ls249"}), Compared{Step: Build, Newest: "4.0.11-ls251"})
	eq(t, Compare("1.0", []string{"2.0", "3.0", "3.1"}).Behind, 2)
	eq(t, Compare("latest", tags), Compared{})
	eq(t, Bigger(Patch, Build), Patch)
	eq(t, Bigger(Current, Major), Major)
}

// The Docker token flow: 401 with a Bearer challenge, an anonymous token, then the tag list (paged) and the digest.
func TestLookup(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			if !strings.HasPrefix(r.URL.Query().Get("scope"), "repository:org/") {
				w.WriteHeader(400)
				return
			}
			w.Write([]byte(`{"token":"t0k"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer t0k" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="test"`)
			w.WriteHeader(401)
			return
		}
		switch {
		case r.URL.Path == "/v2/org/app/tags/list" && r.URL.Query().Get("last") == "":
			w.Header().Set("Link", `</v2/org/app/tags/list?n=1000&last=1.0>; rel="next"`)
			w.Write([]byte(`{"tags":["1.0"]}`))
		case r.URL.Path == "/v2/org/app/tags/list":
			w.Write([]byte(`{"tags":["1.1"]}`))
		case r.URL.Path == "/v2/org/app/manifests/1.0" && r.Method == "HEAD":
			w.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat("b", 64))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	old := client
	client = srv.Client()
	defer func() { client = old }()

	r, ok := Parse(strings.TrimPrefix(srv.URL, "https://") + "/org/app:1.0")
	if !ok {
		t.Fatal("not parsed")
	}
	info, err := Lookup(context.Background(), r, false)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, info, Info{[]string{"1.0", "1.1"}, "sha256:" + strings.Repeat("b", 64)})

	r.Repo = "org/gone"
	if _, err := Lookup(context.Background(), r, false); err != ErrNotFound {
		t.Fatalf("got %v", err)
	}
}
