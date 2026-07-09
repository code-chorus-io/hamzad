# koochooloologin

A small, GoLogin-style **anti-detect browser manager** on the command line.
Each *profile* is an isolated Chrome identity with its own proxy, timezone,
geolocation, and fingerprint. Profiles are stored in a git repository so you can
**share** them with a teammate by pushing to a remote — while the browsing data
(cookies, history) stays on your machine.

It drives an ordinary Chrome/Chromium over the DevTools Protocol (CDP) — no
patched browser — so the reliable overrides (proxy, timezone, geolocation,
user-agent, locale, viewport, isolation) are indistinguishable from a real
Chrome. JS-level fingerprint patches (canvas, WebGL, hardware) are best-effort.

## Install

```sh
go install github.com/1995parham/koochooloologin/cmd/koochooloologin@latest
# or, from a checkout:
just build      # produces ./koochooloologin
```

You also need a Chrome/Chromium binary. It is auto-detected from `PATH`; set
`--chrome-path` or `chrome_path` in the config if it lives elsewhere.

## Quick start

```sh
# create a profile with a proxy and timezone
koochooloologin profile add work \
  --proxy 'socks5://user:pass@1.2.3.4:1080' \
  --timezone Europe/Berlin \
  --screen 1920x1080 \
  --user-agent 'Mozilla/5.0 ...' \
  --canvas-noise

# auto-align timezone & geolocation to the proxy's exit IP, saved to the profile
koochooloologin profile geo work

# launch it — an isolated Chrome window opens with the proxy + overrides applied
koochooloologin profile open work

# ...or derive geo for this session only, and expose a CDP port for automation
koochooloologin profile open work --auto-geo --cdp-port 9222

koochooloologin profile list
koochooloologin profile show work
koochooloologin profile remove work --purge
```

## Sharing profiles (git + age encryption)

The store is a git repository. Secrets and session state are **encrypted with
[age](https://age-encryption.org)** before they are committed, using your team's
public keys — **SSH keys work directly as age recipients**, so there is nothing
new to generate.

| File | Committed? | Contents |
| --- | --- | --- |
| `profiles.toml` | yes, plaintext | non-secret config: fingerprint, timezone, notes |
| `recipients.txt` | yes, plaintext | age/SSH **public** keys allowed to decrypt |
| `secrets/<name>.age` | yes, encrypted | the profile's proxy (incl. credentials) |
| `data/<name>.tar.age` | yes, encrypted | full Chrome user-data-dir (cookies, storage) |
| `data/<name>/` | **gitignored** | the local, unencrypted working copy |

Encrypting needs only the **public** recipients, so `add`/`push` never touch your
private key. Only **decrypting** — `open`, `pull`, `show --reveal` — uses your
identity (`~/.ssh/id_ed25519` by default; override with `--identity`).

```sh
# init seeds recipients.txt with your ~/.ssh/id_ed25519.pub
koochooloologin store init --remote git@github.com:you/profiles.git

# add a teammate so they can decrypt (their SSH public key, or a file of keys)
koochooloologin store recipients add 'ssh-ed25519 AAAA...teammate'
koochooloologin store recipients list

koochooloologin store sync -m "share work profile"   # commit + pull --rebase + push
koochooloologin store status
```

A teammate clones the repo, runs `store sync`, then `profile open work` — their
copy decrypts the proxy and the session bundle and resumes the account.

`profile push` / `profile pull` explicitly re-encrypt / restore a session bundle;
`open` does both automatically (restore before launch, save on exit — disable
with `--no-restore` / `--no-save`).

> After adding new recipients, re-encrypt existing secrets so they can read them
> (`profile add --proxy …` again, or `profile push`). age can only encrypt to
> keys known at encryption time.

## Automation attach

`profile open --cdp-port <port>` fixes Chrome's remote-debugging port and prints
the `webSocketDebuggerUrl`. Point Puppeteer (`connect`), Playwright
(`connectOverCDP`), or Selenium (`debuggerAddress`) at it to automate the profile.

## What is spoofed, and how reliable it is

| Signal | Mechanism | Reliability |
| --- | --- | --- |
| Proxy (HTTP/SOCKS) + auth | `--proxy-server` + CDP `Fetch` auth | solid |
| Timezone | CDP `Emulation.setTimezoneOverride` | solid (engine-native) |
| Geolocation | CDP `setGeolocationOverride` + permission grant | solid |
| User-Agent / platform / Accept-Language | CDP `setUserAgentOverride` / `setLocaleOverride` | solid |
| Viewport / screen | CDP `setDeviceMetricsOverride` | solid |
| Profile isolation | separate `--user-data-dir` | solid |
| hardwareConcurrency / deviceMemory / languages | injected JS (`defineProperty`) | best-effort, detectable |
| Canvas noise / WebGL vendor-renderer | injected JS hooks | best-effort, detectable |

For high-scrutiny targets the JS-patched signals need a patched Chromium
(GoLogin's "Orbita"); the launch layer is designed so such a binary can be
swapped in via `--chrome-path` without changing the CLI.

## Configuration

See [`configs/config.example.toml`](configs/config.example.toml). Every key is
also settable via `KEL_`-prefixed environment variables (e.g. `KEL_STORE_DIR`).

## License

Apache-2.0. See [LICENSE](LICENSE).
