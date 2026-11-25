package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"

	"fleetdeck/internal/db"
	"fleetdeck/internal/httperr"
	"fleetdeck/internal/sec"
)

type Field struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Secret   bool   `json:"secret,omitempty"`
	Optional bool   `json:"optional,omitempty"`
}

// NotifierType is a notification channel kind; its JSON ({label, fields}) is what the UI builds its form from.
type NotifierType struct {
	Key    string  `json:"-"`
	Label  string  `json:"label"`
	Fields []Field `json:"fields"`
	send   func(c map[string]string, title, message, level string) error
}

// TypeList marshals as a JSON object in list order: the UI's type picker shows them in that order.
type TypeList []NotifierType

func (l TypeList) Get(key string) *NotifierType {
	for i := range l {
		if l[i].Key == key {
			return &l[i]
		}
	}
	return nil
}

func (l TypeList) MarshalJSON() ([]byte, error) {
	return ordered(l, func(t NotifierType) (string, any) { return t.Key, t })
}

func post(u string, body any, headers map[string]string) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // as JSON.stringify
	if err := enc.Encode(body); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(bytes.TrimSuffix(buf.Bytes(), []byte("\n"))))
	if err != nil {
		return stripURL(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		return stripURL(err)
	}
	defer r.Body.Close()
	if r.StatusCode < 200 || r.StatusCode > 299 {
		text, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		return fmt.Errorf("HTTP %d %s", r.StatusCode, cut(string(text), 200))
	}
	return nil
}

// stripURL drops the URL from net/http errors: webhook URLs and the Telegram bot token are secrets,
// and these messages end up in the event log and the UI.
func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func priority(level string, alert, normal int) int {
	if level == "error" {
		return alert
	}
	return normal
}

var Types = TypeList{
	{Key: "webhook", Label: "Webhook (JSON POST)", Fields: []Field{{Key: "url", Label: "URL", Secret: true}},
		send: func(c map[string]string, title, message, level string) error {
			return post(c["url"], map[string]any{"title": title, "message": message, "level": level, "time": time.Now().UTC().Format("2006-01-02T15:04:05.000Z")}, nil)
		}},
	{Key: "ntfy", Label: "ntfy", Fields: []Field{{Key: "url", Label: "Topic URL (https://ntfy.sh/my-topic)"}, {Key: "token", Label: "Access token (optional)", Secret: true, Optional: true}},
		send: func(c map[string]string, title, message, level string) error {
			u, err := url.Parse(c["url"])
			if err != nil {
				return stripURL(err)
			}
			path := u.EscapedPath()
			if path == "" {
				path = "/"
			}
			body := map[string]any{"title": title, "message": message, "priority": priority(level, 4, 3)}
			if segs := strings.FieldsFunc(path, func(r rune) bool { return r == '/' }); len(segs) > 0 {
				body["topic"] = segs[len(segs)-1]
			}
			var h map[string]string
			if c["token"] != "" {
				h = map[string]string{"Authorization": "Bearer " + c["token"]}
			}
			return post(u.Scheme+"://"+u.Host+path[:strings.LastIndex(path, "/")]+"/", body, h)
		}},
	{Key: "discord", Label: "Discord", Fields: []Field{{Key: "url", Label: "Webhook URL", Secret: true}},
		send: func(c map[string]string, title, message, _ string) error {
			return post(c["url"], map[string]any{"content": "**" + title + "**\n" + message}, nil)
		}},
	{Key: "slack", Label: "Slack", Fields: []Field{{Key: "url", Label: "Incoming webhook URL", Secret: true}},
		send: func(c map[string]string, title, message, _ string) error {
			return post(c["url"], map[string]any{"text": "*" + title + "*\n" + message}, nil)
		}},
	{Key: "telegram", Label: "Telegram", Fields: []Field{{Key: "token", Label: "Bot token", Secret: true}, {Key: "chat_id", Label: "Chat ID"}},
		send: func(c map[string]string, title, message, _ string) error {
			return post("https://api.telegram.org/bot"+c["token"]+"/sendMessage", map[string]any{"chat_id": c["chat_id"], "text": title + "\n" + message}, nil)
		}},
	// https://pushover.net/api. Alerts use priority 1 (sounds even in the phone's quiet hours; Fleetdeck has its own).
	{Key: "pushover", Label: "Pushover", Fields: []Field{{Key: "token", Label: "Application API token", Secret: true}, {Key: "user", Label: "User or group key", Secret: true},
		{Key: "device", Label: "Device (optional, all devices if empty)", Optional: true}},
		send: func(c map[string]string, title, message, level string) error {
			body := map[string]any{"token": c["token"], "user": c["user"], "title": cut(title, 250), "message": cut(message, 1024), "priority": priority(level, 1, 0)}
			if c["device"] != "" {
				body["device"] = c["device"]
			}
			return post("https://api.pushover.net/1/messages.json", body, nil)
		}},
	{Key: "gotify", Label: "Gotify", Fields: []Field{{Key: "url", Label: "Server URL"}, {Key: "token", Label: "App token", Secret: true}},
		send: func(c map[string]string, title, message, level string) error {
			return post(strings.TrimSuffix(c["url"], "/")+"/message", map[string]any{"title": title, "message": message, "priority": priority(level, 8, 4)},
				map[string]string{"X-Gotify-Key": c["token"]})
		}},
}

// Notifier is a row of notifiers; config_enc is EncryptConfig of the channel's settings.
type Notifier struct {
	ID        string `db:"id" json:"id"`
	TenantID  string `db:"tenant_id" json:"-"`
	Name      string `db:"name" json:"name"`
	Type      string `db:"type" json:"type"`
	ConfigEnc string `db:"config_enc" json:"-"`
	Enabled   int    `db:"enabled" json:"enabled"`
	CreatedAt int64  `db:"created_at" json:"created_at"`
}

func decryptConfig(blob string) (map[string]string, error) {
	plain, err := sec.Decrypt(blob)
	if err != nil {
		return nil, err
	}
	c := map[string]string{}
	return c, json.Unmarshal([]byte(plain), &c)
}

// EncryptConfig is how notifier settings are stored (notifiers.config_enc).
func EncryptConfig(c map[string]string) string {
	b, _ := json.Marshal(c)
	return sec.Encrypt(string(b))
}

// NotifierInput is a validated notifier: store Config with EncryptConfig.
type NotifierInput struct {
	Name    string
	Type    string
	Config  map[string]string
	Enabled int
}

var urlKey, httpURL = regexp.MustCompile(`(?i)url`), regexp.MustCompile(`^https?://`)

// ValidateNotifier checks a channel from a request body. On edit (existing != nil), blank secrets keep their
// stored value and a body without "config" keeps the whole config.
func ValidateNotifier(b map[string]any, existing *Notifier) (*NotifierInput, error) {
	typ, _ := b["type"].(string)
	t := Types.Get(typ)
	if t == nil {
		return nil, httperr.Bad("unknown notifier type")
	}
	name := strings.TrimSpace(orEmpty(b["name"]))
	if name == "" || len(utf16.Encode([]rune(name))) > 100 {
		return nil, httperr.Bad("name required")
	}
	old := map[string]string{}
	if existing != nil {
		var err error
		if old, err = decryptConfig(existing.ConfigEnc); err != nil {
			return nil, err
		}
		if _, ok := b["config"]; !ok {
			return &NotifierInput{name, typ, old, enabled(b)}, nil
		}
	}
	in, _ := b["config"].(map[string]any)
	config := map[string]string{}
	for _, f := range t.Fields {
		v := ""
		if x := in[f.Key]; x != nil {
			v = strings.TrimSpace(jsStr(x))
		}
		if v == "" && f.Secret && old[f.Key] != "" {
			v = old[f.Key] // blank secret on edit = keep
		}
		if v == "" && !f.Optional {
			return nil, httperr.Bad(f.Label + " is required")
		}
		if v != "" && urlKey.MatchString(f.Key) && !httpURL.MatchString(v) {
			return nil, httperr.Bad(f.Label + " must start with http:// or https://")
		}
		config[f.Key] = v
	}
	return &NotifierInput{name, typ, config, enabled(b)}, nil
}

type View struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Type    string            `json:"type"`
	Enabled bool              `json:"enabled"`
	Config  map[string]string `json:"config"`
}

// NotifierView is what the UI gets. Secrets never leave the server; the UI only learns whether they are set.
func NotifierView(n *Notifier) (*View, error) {
	t := Types.Get(n.Type)
	if t == nil {
		return nil, fmt.Errorf("unknown notifier type %q", n.Type)
	}
	c, err := decryptConfig(n.ConfigEnc)
	if err != nil {
		return nil, err
	}
	config := map[string]string{}
	for _, f := range t.Fields {
		if f.Secret {
			config[f.Key] = ""
			if c[f.Key] != "" {
				config[f.Key] = "••••••"
			}
		} else if v, ok := c[f.Key]; ok {
			config[f.Key] = v
		}
	}
	return &View{n.ID, n.Name, n.Type, n.Enabled != 0, config}, nil
}

// SendOne sends through one channel and waits (10s timeout).
func SendOne(n *Notifier, title, message, level string) error {
	t := Types.Get(n.Type)
	if t == nil {
		return fmt.Errorf("unknown notifier type %q", n.Type)
	}
	c, err := decryptConfig(n.ConfigEnc)
	if err != nil {
		return err
	}
	return t.send(c, title, message, level)
}

// Notify sends through every enabled channel of the organization in the background; failures go to the event log.
// level: info, success or error.
func Notify(tenant, title, message, level string) {
	ns, err := db.Select[Notifier](`SELECT * FROM notifiers WHERE tenant_id = ? AND enabled = 1`, tenant)
	if err != nil {
		log.Printf("notify: %v", err)
		return
	}
	for _, n := range ns {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("notify via %s: %v", n.Name, r)
				}
			}()
			if err := SendOne(&n, title, message, level); err != nil {
				db.Event("notify", fmt.Sprintf(`Notification via "%s" failed: %s`, n.Name, err), db.EventOpts{Level: "error", TenantID: n.TenantID})
			}
		}()
	}
}
