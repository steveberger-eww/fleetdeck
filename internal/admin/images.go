package admin

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"fleetdeck/internal/db"
	"fleetdeck/internal/registry"
)

// ImageUpdate compares one container's image with what its registry offers now.
type ImageUpdate struct {
	ID       string `json:"id"`      // container id
	Ref      string `json:"ref"`     // the image as the container names it
	Version  string `json:"version"` // what runs: the tag when it names a version, else the image's version label
	Step     string `json:"step"`    // registry.Current … registry.Major; "" = couldn't tell (Error says why)
	Newest   string `json:"newest,omitempty"`
	Safe     string `json:"safe,omitempty"` // newest tag with the running major version
	Behind   int    `json:"behind,omitempty"`
	Channel  bool   `json:"channel"`            // the tag is a channel (latest, stable), not a version: pulling it again gets Newest
	NewBuild bool   `json:"newBuild,omitempty"` // the tag points at a newer image than the one on the server
	Pulled   bool   `json:"pulled,omitempty"`   // that newer image is already on the server: recreating applies it
	Pinned   bool   `json:"pinned,omitempty"`   // the reference pins a digest
	Error    string `json:"error,omitempty"`
}

type localImage struct {
	digests map[string]bool // RepoDigests, digest part
	version string          // org.opencontainers.image.version
}

// Containers' image references and image ids, the images' digests and config (for the version label: images
// without labels make {{.Config.Labels}} fail the template), and which image each
// reference names on the server now (differs from the container's after a pull without recreate).
const imagesScript = `command -v docker >/dev/null 2>&1 || exit 0
ids=$(docker ps -aq --no-trunc)
[ -n "$ids" ] || exit 0
docker inspect --format '{{json .Id}} {{json .Config.Image}} {{json .Image}}' $ids
echo '#images'
docker image inspect --format '{{json .Id}} {{json .RepoDigests}} {{json .Config}}' $(docker inspect --format '{{.Image}}' $ids | sort -u) 2>/dev/null
echo '#refs'
docker inspect --format '{{.Config.Image}}' $ids | sort -u | while IFS= read -r r; do
  printf '%s\t%s\n' "$r" "$(docker image inspect --format '{{.Id}}' -- "$r" 2>/dev/null)"
done`

// values decodes the JSON values on one line ("a" ["b"] "c").
func values(line string, into ...any) bool {
	d := json.NewDecoder(strings.NewReader(line))
	for _, v := range into {
		if d.Decode(v) != nil {
			return false
		}
	}
	return true
}

type ctrImage struct{ id, ref, image string }

func parseImages(out string) (ctrs []ctrImage, images map[string]localImage, refs map[string]string) {
	images, refs = map[string]localImage{}, map[string]string{}
	section := ""
	for _, l := range strings.Split(out, "\n") {
		if l == "#images" || l == "#refs" {
			section = l
			continue
		}
		switch section {
		case "":
			var c ctrImage
			if values(l, &c.id, &c.ref, &c.image) {
				ctrs = append(ctrs, c)
			}
		case "#images":
			var id string
			var digests []string
			var cfg struct{ Labels map[string]string }
			if values(l, &id, &digests, &cfg) {
				li := localImage{map[string]bool{}, cfg.Labels["org.opencontainers.image.version"]}
				for _, d := range digests {
					if _, dg, ok := strings.Cut(d, "@"); ok {
						li.digests[dg] = true
					}
				}
				images[id] = li
			}
		case "#refs":
			if ref, id, ok := strings.Cut(l, "\t"); ok && id != "" {
				refs[ref] = id
			}
		}
	}
	return
}

type answer struct {
	info registry.Info
	err  error
}

// ImageUpdates checks every container's image. Registry answers are cached; refresh asks again.
func ImageUpdates(ctx context.Context, s *db.Server, refresh bool) ([]ImageUpdate, error) {
	out, err := sh(ctx, s, imagesScript, shOpts{timeout: 60 * time.Second})
	if err != nil {
		return nil, err
	}
	ctrs, images, refs := parseImages(out)

	// One registry lookup per reference, a few at a time. Images built on the server have nothing to look up.
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	answers := map[string]*answer{}
	var wg sync.WaitGroup
	gate := make(chan struct{}, 4)
	for _, c := range ctrs {
		r, ok := registry.Parse(c.ref)
		if _, seen := answers[c.ref]; !ok || seen || len(images[c.image].digests) == 0 {
			continue
		}
		a := &answer{}
		answers[c.ref] = a
		wg.Go(func() {
			gate <- struct{}{}
			defer func() { <-gate }()
			a.info, a.err = registry.Lookup(ctx, r, refresh)
		})
	}
	wg.Wait()

	res := []ImageUpdate{}
	for _, c := range ctrs {
		res = append(res, judge(c, images[c.image], refs[c.ref], answers[c.ref]))
	}
	return res, nil
}

// judge compares one container with its registry's answer (nil: nothing was asked).
func judge(c ctrImage, img localImage, refID string, a *answer) ImageUpdate {
	u := ImageUpdate{ID: c.id, Ref: c.ref, Pulled: refID != "" && refID != c.image}
	if u.Pulled {
		u.Step = registry.Build
	}
	r, ok := registry.Parse(c.ref)
	switch {
	case !ok:
		u.Error = "Started from an image id, so there's no tag to compare"
		return u
	case len(img.digests) == 0:
		u.Error = "Built on the server, so there's no registry image to compare"
		return u
	case a == nil:
		return u
	case a.err != nil:
		u.Error = a.err.Error()
		return u
	}
	u.Pinned = r.Digest != ""
	u.NewBuild = !u.Pinned && a.info.Digest != "" && !img.digests[a.info.Digest]
	if u.Step == "" {
		u.Step = registry.Current
	}
	if u.NewBuild {
		u.Step = registry.Bigger(u.Step, registry.Build)
	}
	var cmp registry.Compared
	switch {
	case registry.IsVersion(r.Tag):
		u.Version = r.Tag
		cmp = registry.Compare(r.Tag, a.info.Tags)
	default:
		// A channel (latest, stable): the image's version label says what runs. Only a new image behind the tag
		// means there is something to get, and pulling the same tag gets it.
		u.Version, u.Channel = img.version, true
		if u.NewBuild {
			cmp = registry.Compare(img.version, a.info.Tags)
		}
	}
	u.Newest, u.Safe, u.Behind = cmp.Newest, cmp.Safe, cmp.Behind
	u.Step = registry.Bigger(u.Step, cmp.Step)
	return u
}
