# elagoht/flash

A collage plugin for flash messages: a message an action sets before it
redirects, shown once by the page it redirects to.

```go
app, err := collage.New(&collage.Config{
	Plugins: []collage.Plugin{flash.New(flash.Options{})},
})
```

Requires collage v0.61.0 or later. Register it in `Config.Plugins`: it adds a
template function, which only a plugin registered there can.

## Setting and showing one

An accepted form redirects, so a reload does not submit it twice — and the page it
redirects to has no way to say "saved" but this:

```go
func save(ctx context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
	if err := settings.Save(ctx, rc.Request); err != nil {
		return nil, err
	}
	flash.Add(rc, flash.Success, "Your changes are saved.")
	return collage.SeeOther("/settings"), nil
}
```

The layout shows what there is, and a message shown is gone:

```html
{{range flashes}}
  <p class="flash flash--{{.Kind}}" role="status">{{.Text}}</p>
{{end}}
```

`Kind` is a free string for the template to style by; `flash.Success`, `Info`,
`Warning` and `Error` are the usual ones. `Text` is text, escaped like anything
else. A handler of your own adds one with `flash.AddTo(r.Context(), kind, text)`.

A message not yet shown is kept when another is added, so a redirect through a page
that shows none does not lose it. One that is never shown is dropped after
`maxAge` (default `"5m"`).

## The cookie, and the cache

The messages travel in a cookie, `HttpOnly`, `SameSite=Lax`, and signed with
HMAC-SHA256: a reader cannot put words in the site's mouth. At most eight messages
of 300 characters are kept, which stays inside the four kilobytes a browser keeps
of a cookie.

A request carrying a message is answered with a fresh render that is neither read
from the page cache nor written to it, and marked `private, no-store`: a cached
page showing one reader's message would show it to the next. Every other request
is served as it would be without the plugin.

## The key

Set a key of at least 32 random bytes, the same on every instance and across
restarts. It is a secret, so it never goes into `plugins-config.json`: put it,
hex-encoded, in the environment variable `FLASH_KEY` (`openssl rand -hex 32`
makes one), which is read with nothing configured:

```sh
FLASH_KEY=…  # in .env.development, and where production runs
```

To read another variable, name it in `keyEnv`:

```json
{ "elagoht/flash": { "keyEnv": "MY_FLASH_KEY" } }
```

A variable named but empty, not hex, or shorter than 32 bytes stops the app;
`FLASH_KEY` may be unset or empty, and not hex or too short it stops the app
too. From Go, set `flash.Options{Key: key}` instead; `keyEnv`, when set, is used
over it, and it is used over `FLASH_KEY`.

Without one, a key is generated per process and a warning logged: a message set
just before a restart, or on another instance, is dropped.

## Configuration

```json
{
  "elagoht/flash": {
    "keyEnv": "",
    "cookie": "collage_flash",
    "maxAge": "5m"
  }
}
```

| Option | JSON | Default | |
| --- | --- | --- | --- |
| `KeyEnv` | `keyEnv` | `FLASH_KEY` | The environment variable holding the key, hex-encoded |
| `Key` | Go only | from `FLASH_KEY`, else generated per process | The key, 32 bytes or more |
| `Cookie` | `cookie` | `"collage_flash"` | The cookie's name |
| `MaxAge` | `maxAge` | `"5m"` | How long an unshown message is kept, at least `"1s"`; a duration string |

The cookie is `Secure` when the request came over HTTPS, as `collage.IsHTTPS`
decides: `X-Forwarded-Proto` counts only from `Server.TrustedProxies`.

## Changes

### Changed in v0.2.0

| Was | Now |
| --- | --- |
| `"key"` (hex in `plugins-config.json`) / `Options.KeyHex` | `"keyEnv"`, naming an environment variable that holds the hex key / `Options.KeyEnv`; `Options.Key` from Go is unchanged |
| `"maxAge": 300` (integer seconds) / `MaxAge int` | `"maxAge": "5m"` / `MaxAge collage.Duration` |

From Go, write `MaxAge: collage.Duration(5 * time.Minute)`: an untyped `300` now
means 300ns and stops startup (a `maxAge` under one second is refused).

An old `"key"` now stops the app with `unknown key "key"`, and an integer `maxAge`
with an error asking for a duration string. Requires collage v0.61.0 (it was built
against v0.50.0). The cookie's `Secure` flag follows `collage.IsHTTPS`, so a
forged `X-Forwarded-Proto` from outside `Server.TrustedProxies` no longer sets it.
