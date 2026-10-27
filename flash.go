// Package flash is a collage plugin for flash messages: a message an action sets
// before it redirects, shown once by the page it redirects to.
//
//	app, err := collage.New(&collage.Config{
//		Plugins: []collage.Plugin{flash.New(flash.Options{})},
//	})
//
// with the signing key in an environment variable named by "keyEnv" in
// plugins-config.json, or set from Go:
//
//	app, err := collage.New(&collage.Config{
//		Plugins: []collage.Plugin{flash.New(flash.Options{Key: key})},
//	})
//
// The action adds the message and redirects, as an accepted form should:
//
//	func save(ctx context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
//		// ... save ...
//		flash.Add(rc, flash.Success, "Your changes are saved.")
//		return collage.SeeOther("/settings"), nil
//	}
//
// and the layout shows what there is:
//
//	{{range flashes}}<p class="flash flash--{{.Kind}}" role="status">{{.Text}}</p>{{end}}
//
// The message travels in a cookie, signed so a reader cannot put words in the
// site's mouth. A request carrying one is answered with a fresh render that is
// neither read from the page cache nor written to it: a cached page showing one
// reader's message would show it to the next.
package flash

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// Name is the plugin's name, and the key its configuration is found under.
const Name = "elagoht/flash"

// Kinds a message is commonly one of. Kind is a free string, used by the template
// to style the message; these are only the usual ones.
const (
	Success = "success"
	Info    = "info"
	Warning = "warning"
	Error   = "error"
)

// Message is one flash message.
type Message struct {
	Kind string `json:"k"`
	Text string `json:"t"`
}

// Options configures the plugin.
type Options struct {
	// KeyEnv names the environment variable holding the signing key, hex-encoded
	// (`openssl rand -hex 32`). When set, the variable must hold a key, and it is
	// used instead of Key. Unset, FLASH_KEY (DefaultKeyEnv) is read.
	KeyEnv string `json:"keyEnv"`
	// Key signs the cookie from Go: at least 32 random bytes, the same on every
	// instance and across restarts. With neither KeyEnv nor Key, the key is read
	// from FLASH_KEY (DefaultKeyEnv); with that empty too, one is generated per
	// process, and a message set just before a restart, or on another instance,
	// is dropped.
	Key []byte `json:"-"`
	// Cookie is the cookie's name. Default "collage_flash".
	Cookie string `json:"cookie"`
	// MaxAge is how long an unshown message waits before it is dropped, at
	// least "1s". Default "5m".
	MaxAge collage.Duration `json:"maxAge"`
}

// DefaultKeyEnv is the environment variable the key is read from when neither
// KeyEnv nor Key is set. Unlike one KeyEnv names, it may be unset or empty.
const DefaultKeyEnv = "FLASH_KEY"

// Limits on what is kept. They count messages and characters, not the bytes a
// message takes once encoded, so the cookie is also measured as it is written:
// maxCookie is the most a whole Set-Cookie line may be — name, value and
// attributes — within the four kilobytes a browser keeps of one cookie and drops
// whole past.
const (
	maxMessages = 8
	maxText     = 300
	maxCookie   = 4096
)

// Plugin carries the messages.
type Plugin struct {
	opts Options
	log  *slog.Logger
}

// New returns a plugin with opts as its starting point, which the application's
// own configuration is then decoded over.
func New(opts Options) *Plugin { return &Plugin{opts: opts} }

func (p *Plugin) Name() string                   { return Name }
func (p *Plugin) Version() string                { return "0.3.1" }
func (p *Plugin) Shutdown(context.Context) error { return nil }

// Configure reads the configuration and adds {{flashes}}.
func (p *Plugin) Configure(_ context.Context, host collage.ConfigHost) error {
	var err error
	if p.opts, err = collage.PluginConfig(host, p.opts); err != nil {
		return err
	}
	p.log = host.Logger()
	if name := p.opts.KeyEnv; name != "" {
		value := os.Getenv(name)
		if value == "" {
			return fmt.Errorf("flash: the environment variable %s is empty", name)
		}
		key, err := hex.DecodeString(value)
		if err != nil {
			return fmt.Errorf("flash: the environment variable %s is not hex", name)
		}
		p.opts.Key = key
	} else if value := os.Getenv(DefaultKeyEnv); value != "" && len(p.opts.Key) == 0 {
		key, err := hex.DecodeString(value)
		if err != nil {
			return fmt.Errorf("flash: the environment variable %s is not hex", DefaultKeyEnv)
		}
		p.opts.Key = key
	}
	if len(p.opts.Key) == 0 {
		p.opts.Key = make([]byte, 32)
		if _, err := rand.Read(p.opts.Key); err != nil {
			return fmt.Errorf("flash: %w", err)
		}
		p.log.Warn("flash: no key set (" + DefaultKeyEnv + " is empty), so one was generated for this process; a message set before a restart, or on another instance, is dropped")
	} else if len(p.opts.Key) < 32 {
		return errors.New("flash: the key must be at least 32 bytes")
	}
	if p.opts.Cookie == "" {
		p.opts.Cookie = "collage_flash"
	}
	switch {
	case p.opts.MaxAge == 0:
		p.opts.MaxAge = collage.Duration(5 * time.Minute)
	case p.opts.MaxAge < collage.Duration(time.Second):
		return fmt.Errorf("flash: maxAge %s must be at least 1s", p.opts.MaxAge)
	}
	return host.AddRenderFunc("flashes", func(rc *collage.RenderContext) any { // any: html/template.FuncMap's own value type
		return func() []Message { return Take(rc) }
	})
}

// Init wraps every request, to read the cookie and write it back.
func (p *Plugin) Init(_ context.Context, host collage.Host) error {
	if p.opts.Cookie == "" {
		return errors.New("flash: register the plugin in Config.Plugins, where Configure runs; {{flashes}} needs it")
	}
	return host.Use(p.middleware)
}

// box holds one request's messages: those it arrived with, and those it adds.
type box struct {
	mu       sync.Mutex
	incoming []Message
	taken    bool
	outgoing []Message
}

type boxKey struct{}

func boxOf(ctx context.Context) *box {
	if ctx == nil {
		return nil
	}
	b, _ := ctx.Value(boxKey{}).(*box)
	return b
}

// Add queues a message for the next page this reader sees: the one the action
// redirects to. Outside a request the plugin wraps — a static build, a test with
// no plugin — it does nothing.
func Add(rc *collage.RenderContext, kind, text string) {
	if rc == nil {
		return
	}
	AddTo(rc.Context(), kind, text)
}

// AddTo is Add for a handler of your own, given its request's context.
func AddTo(ctx context.Context, kind, text string) {
	b := boxOf(ctx)
	if b == nil {
		return
	}
	if r := []rune(text); len(r) > maxText {
		text = string(r[:maxText])
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.outgoing = append(b.outgoing, Message{Kind: kind, Text: text})
	if len(b.outgoing) > maxMessages {
		b.outgoing = b.outgoing[len(b.outgoing)-maxMessages:]
	}
}

// Take returns the messages this request arrived with and marks them shown: the
// response clears them. {{flashes}} is Take.
func Take(rc *collage.RenderContext) []Message {
	if rc == nil {
		return nil
	}
	b := boxOf(rc.Context())
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.taken = true
	return append([]Message(nil), b.incoming...)
}

func (p *Plugin) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := &box{}
		if cookie, err := r.Cookie(p.opts.Cookie); err == nil {
			if messages, ok := p.decode(cookie.Value); ok && len(messages) > 0 {
				b.incoming = messages
				// A page showing this reader's message must not be the page the
				// cache hands the next reader.
				_ = collage.SkipCache(r)
			}
		}
		fw := &flashWriter{ResponseWriter: w, plugin: p, box: b, request: r}
		next.ServeHTTP(fw, r.WithContext(context.WithValue(r.Context(), boxKey{}, b)))
		fw.commit()
	})
}

// flashWriter sets or clears the cookie before the headers go out.
type flashWriter struct {
	http.ResponseWriter
	plugin    *Plugin
	box       *box
	request   *http.Request
	committed bool
}

func (w *flashWriter) commit() {
	if w.committed {
		return
	}
	w.committed = true
	b := w.box
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case len(b.outgoing) > 0:
		// Unshown messages ride along with new ones, so a redirect through a page
		// that shows none does not lose them.
		messages := b.outgoing
		if !b.taken {
			messages = append(append([]Message(nil), b.incoming...), b.outgoing...)
			if len(messages) > maxMessages {
				messages = messages[len(messages)-maxMessages:]
			}
		}
		if c := w.plugin.fit(w.request, messages); c != nil {
			http.SetCookie(w.ResponseWriter, c)
		} else if len(b.incoming) > 0 {
			http.SetCookie(w.ResponseWriter, w.plugin.cookie(w.request, "", -1))
		}
	case b.taken && len(b.incoming) > 0:
		http.SetCookie(w.ResponseWriter, w.plugin.cookie(w.request, "", -1))
	}
}

func (w *flashWriter) WriteHeader(status int) {
	w.commit()
	w.ResponseWriter.WriteHeader(status)
}

func (w *flashWriter) Write(b []byte) (int, error) {
	w.commit()
	return w.ResponseWriter.Write(b)
}

// Flush passes a flush through.
func (w *flashWriter) Flush() {
	w.commit()
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the connection.
func (w *flashWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (p *Plugin) cookie(r *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     p.opts.Cookie,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   collage.IsHTTPS(r),
	}
}

// fit is the cookie carrying messages, within maxCookie bytes. Past it, the
// oldest messages are dropped and, when one alone is too big, its text is cut
// short; a message that does not fit even without text is dropped, and fit
// returns nil when none is left. Anything it drops or cuts is logged, since
// the reader will not see it.
func (p *Plugin) fit(r *http.Request, messages []Message) *http.Cookie {
	maxAge := int(time.Duration(p.opts.MaxAge) / time.Second)
	dropped, cut := 0, false
	defer func() {
		if (dropped > 0 || cut) && p.log != nil {
			p.log.Warn("flash: messages do not fit the 4096-byte cookie a browser keeps; the oldest were dropped or cut short",
				"dropped", dropped, "cut", cut)
		}
	}()
	for len(messages) > 0 {
		c := p.cookie(r, p.encode(messages), maxAge)
		over := len(c.String()) - maxCookie
		if over <= 0 {
			return c
		}
		if len(messages) > 1 {
			messages = messages[1:]
			dropped++
			continue
		}
		text := []rune(messages[0].Text)
		if len(text) == 0 {
			dropped++
			return nil
		}
		// A character is at most eight bytes once JSON-escaped and base64'd, so
		// cutting over/8 of them, at least one, never cuts more than needed by
		// much, and the loop measures again.
		cut = true
		messages = []Message{{Kind: messages[0].Kind, Text: string(text[:len(text)-min(len(text), max(1, over/8))])}}
	}
	return nil
}

// encode signs messages: base64url(JSON) "." base64url(HMAC-SHA256).
func (p *Plugin) encode(messages []Message) string {
	body, _ := json.Marshal(messages)
	payload := base64.RawURLEncoding.EncodeToString(body)
	return payload + "." + base64.RawURLEncoding.EncodeToString(p.sign(payload))
}

func (p *Plugin) decode(value string) ([]Message, bool) {
	payload, signature, ok := strings.Cut(value, ".")
	if !ok {
		return nil, false
	}
	sig, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || !hmac.Equal(sig, p.sign(payload)) {
		return nil, false
	}
	body, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, false
	}
	var messages []Message
	if json.Unmarshal(body, &messages) != nil {
		return nil, false
	}
	return messages, true
}

func (p *Plugin) sign(payload string) []byte {
	mac := hmac.New(sha256.New, p.opts.Key)
	mac.Write([]byte("collage-flash:" + payload))
	return mac.Sum(nil)
}
