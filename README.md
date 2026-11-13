# hamzad

A small, GoLogin-style **anti-detect browser manager** on the command line. Each *profile* is an isolated Chrome identity with its own proxy, timezone, geolocation, and fingerprint. Profiles are stored in a git repository so you can **share** them with a teammate by pushing to a remote — while the browsing data (cookies, history) stays on your machine.

It drives an ordinary Chrome/Chromium — no patched browser. By default a profile launches **clean**: a plain child process with no DevTools (CDP) session and no remote-debugging port, so none of the automation tells that block sites like Google sign-in are present. The tradeoff is that overrides needing a live CDP session — `navigator.platform`, Accept-Language, geolocation, screen metrics, and every JS-level fingerprint patch — are *not* applied. Pass `--cdp-port` to get them. See [what is spoofed](#what-is-spoofed-and-how-reliable-it-is) for the per-signal split; `profile open` prints which overrides it had to drop.

## Install

Prebuilt static binaries for Linux, macOS, and Windows (amd64 and arm64) are attached to every [release](https://github.com/code-chorus-io/hamzad/releases). Download the archive for your platform, check it against the release's `checksums.txt`, and put `hamzad` on your `PATH`.

```sh
go install github.com/code-chorus-io/hamzad/cmd/hamzad@latest
# or, from a checkout:
just build      # produces ./hamzad
just snapshot   # builds the release archives into ./dist, publishing nothing
```

You also need a browser. Either bring your own — auto-detected from `PATH`, or set `--chrome-path` / `chrome_path` — or let hamzad fetch one:

```sh
hamzad browser install stable   # ~190 MB, Chrome for Testing
hamzad browser list
hamzad browser path             # which binary a launch would use
```

Downloads go to `~/.cache/hamzad/browsers/<version>/` and never auto-update. Nothing is fetched implicitly: `profile open` will not pull a browser mid-command, it tells you which `browser install` to run.

Pinning a version with `chrome_version` is worth doing. A profile's user-agent is only convincing while it matches the engine actually rendering the page, and a teammate resuming a shared profile on their own Chrome is running a subtly different identity than the one you shared.

**Why Chrome for Testing and not Chromium.** Chromium's openness buys nothing without recompiling it, and a stock Chromium binary is *more* identifiable than Chrome: no proprietary codecs (H.264/AAC), `"Chromium"` rather than `"Google Chrome"` in its user-agent client-hint brands, and no Widevine — three signals a fingerprinter reads for free. Chrome for Testing is the same browser Google ships to users, just pinned and packaged for automation. If you want engine-level spoofing, the answer is a *patched* Chromium you build yourself (GoLogin's "Orbita" and friends), pointed at with `--chrome-path`.

On Linux the archive carries no system libraries, so a minimal desktop or container needs a handful installed separately. `browser install` runs the binary once and tells you exactly which are missing rather than leaving it to fail at launch. There is no `linux-arm64` build — Google publishes none — so on that platform bring your own browser.

## Quick start

```sh
# create a profile with a proxy and timezone
hamzad profile add work \
  --proxy 'socks5://user:pass@1.2.3.4:1080' \
  --timezone Europe/Berlin \
  --screen 1920x1080 \
  --user-agent 'Mozilla/5.0 ...' \
  --canvas-noise

# auto-align timezone & geolocation to the proxy's exit IP, saved to the profile
hamzad profile geo work

# launch it — an isolated Chrome window opens with the proxy + overrides applied
hamzad profile open work

# the geolocation saved above needs a CDP session to take effect; the timezone
# applies either way. --cdp-port also exposes the port for automation to attach.
hamzad profile open work --cdp-port 9222

# ...or derive geo for this session only, without saving it to the profile
hamzad profile open work --auto-geo --cdp-port 9222

hamzad profile list
hamzad profile show work

# change only what you name; '' clears a text setting
hamzad profile edit work --timezone America/Chicago --notes ''
hamzad profile edit work --proxy 'http://user:pass@5.6.7.8:8080'
hamzad profile edit work --geo '41.70,-88.19'   # or --geo '' to clear it

hamzad profile remove work --purge
```

`profile geo` and `--auto-geo` ask [ipwho.is](https://ipwho.is) over HTTPS, from behind the profile's own proxy. TLS matters here: the question travels through the very proxy being measured, so over plain HTTP its operator could choose the answer the profile then pins. The lookup egresses through the same relay Chrome uses, so it measures the exit of whatever protocol the profile actually speaks.

## Proxies

A profile's proxy is one string, encrypted in the store. It can be a **share link** — the thing a provider hands you — or a **raw [sing-box](https://sing-box.sagernet.org) outbound JSON object** for anything a link cannot express.

```sh
hamzad profile add a --proxy 'socks5://user:pass@1.2.3.4:1080'
hamzad profile add b --proxy 'vless://UUID@host:443?security=reality&pbk=KEY&sid=01ab&fp=chrome&sni=www.apple.com'
hamzad profile add c --proxy 'trojan://password@host:443?sni=cdn.example.com'
hamzad profile add d --proxy '{"type":"hysteria2","server":"h.example.com","server_port":443,"password":"pw","tls":{"enabled":true}}'
```

Share links are parsed for `socks5`/`socks4`/`http`/`https`, `vless`, `trojan` and `ss`; everything else sing-box supports — hysteria2, tuic, wireguard, shadowtls, anytls, ssh — goes in as a raw outbound object. That escape hatch is also how you reach options no link can carry: multiplex, fragment, custom dialers.

Whatever the protocol, Chrome only ever sees an unauthenticated HTTP proxy on loopback. Chrome cannot authenticate a proxy from the command line and cannot speak SOCKS auth at all, so sing-box runs the real handshake behind that listener. A share link's `fp=chrome` is honoured: the TLS ClientHello is made to look like Chrome's, which matters as much as the address does.

### Checking a proxy

`proxy check` tests proxies without opening a browser. It goes through the same sing-box relay that Chrome would use, so it speaks every protocol a profile can.

```sh
hamzad proxy check                 # every profile that has a proxy
hamzad proxy check work home       # only these
hamzad proxy check --spec 'vless://…'   # a link, before you add it
hamzad proxy check --json
```

For each proxy it shows the exit IP, the country and the timezone, and two latencies. `CONNECT` is the first request: proxy handshake, TLS and one round trip, which is what a fresh page load pays. `RTT` is the median of three more requests on the open connection. The status is `ok`, `FAIL`, or `mismatch` when the profile disagrees with its exit in a way a site can see:

- its timezone has a different UTC offset than the exit IP (two zones with the same offset, like Berlin and Paris, are not a mismatch),
- it pins no timezone, so the browser shows this machine's,
- its pinned geolocation is more than 500 km from the exit IP's location.

`profile geo` fixes the first two. The command exits non-zero if any proxy fails, so you can use it in a script or a cron job. The exit location comes from ipwho.is, asked through the proxy over HTTPS, the same as `profile geo`.

## Named stores

Profiles live in a **store**, and you can have several. Each is its own git repository with its own profiles, recipients and session bundles, so a work set shared with colleagues never mixes with a personal one.

```sh
hamzad --store work store init --remote git@github.com:acme/profiles.git
hamzad --store work profile add client-a --proxy 'socks5://…'

hamzad --store personal store init
hamzad --store personal profile add shopping

hamzad store list          # which stores exist, and which is active
```

Stores live under `hamzad/stores/<name>` in your platform's config directory — `$XDG_CONFIG_HOME` on Linux, `~/Library/Application Support` on macOS, `%AppData%` on Windows. Omitting `--store` uses `default`. Set it per shell with `HAMZAD_STORE=work`, or pin one in the config with `store = "work"`. For a store kept outside the config root — a shared checkout, an encrypted volume — `--store-dir /path` addresses it directly and overrides the name.

An installation that predates named stores, with its store sitting directly in the config root, keeps working as-is; naming a store is what opts you into the new layout.

## Sharing profiles (git + age encryption)

The store is a git repository. Secrets and session state are **encrypted with [age](https://age-encryption.org)** before they are committed, using your team's public keys — **SSH keys work directly as age recipients**, so there is nothing new to generate.

| File | Committed? | Contents |
| --- | --- | --- |
| `profiles.toml` | yes, plaintext | non-secret config: fingerprint, timezone, notes |
| `recipients.txt` | yes, plaintext | age/SSH **public** keys allowed to decrypt |
| `secrets/<name>.age` | yes, encrypted | the profile's proxy (incl. credentials) |
| `data/<name>.tar.age` | yes, encrypted | Chrome user-data-dir minus caches (cookies, storage, logins), plus the cookies as portable JSON |
| `data/<name>/` | **gitignored** | the local, unencrypted working copy |

Encrypting needs only the **public** recipients, so `add`/`edit`/`push` never touch your private key. Only **decrypting** — `open`, `pull`, `show --reveal` — uses your identity (`~/.ssh/id_ed25519` by default; override with `--identity`).

An SSH key with a passphrase is asked for it once per command, however many files that command decrypts, and a mistyped passphrase gets three tries. With no terminal attached — a launcher, a script, an agent — set `askpass` (`HAMZAD_ASKPASS`, `--askpass`) to a program that prints the passphrase; it is run with the prompt as its argument, like `SSH_ASKPASS`. On macOS, set `keychain = true` (`HAMZAD_KEYCHAIN=true`, `--keychain`) to keep the passphrase in the login Keychain instead: the first command asks for it as usual and saves it, and every later command reads it from there, terminal or not. The item is a generic password with service `hamzad` and the identity's absolute path as its account; delete it in Keychain Access, or with `security delete-generic-password -s hamzad -a <path>`, to forget it. If the stored passphrase stops working, hamzad asks again and replaces it.

The bundle deliberately leaves out Chrome's caches — `Cache`, `Code Cache`, `GPUCache`, the shader and Service-Worker caches. Chrome rebuilds them on next launch and none of them carries any part of the identity being shared, but they dominate a profile's size: a real signed-in profile measured 197 MB packed with them and 66 MB without. That matters because the bundle is encrypted, so it is incompressible and git cannot delta two revisions — every `profile push` commits the whole file again, against GitHub's 50 MB warning and 100 MB hard block. If a profile still outgrows the limit, put `data/*.age` behind [Git LFS](https://git-lfs.com).

```sh
# init seeds recipients.txt with your ~/.ssh/id_ed25519.pub
hamzad store init --remote git@github.com:you/profiles.git

# add a teammate so they can decrypt (their SSH public key, or a file of keys)
# --reencrypt rewrites what is already stored so the new key can read it too
hamzad store recipients add --reencrypt 'ssh-ed25519 AAAA...teammate'
hamzad store recipients list

hamzad store sync -m "share work profile"   # commit + pull --rebase + push
hamzad store status
```

A teammate clones the repo, runs `store sync`, then `profile open work` — their copy decrypts the proxy and the session bundle and resumes the account.

`profile push` / `profile pull` explicitly re-encrypt / restore a session bundle; `open` does both automatically (restore before launch, save on exit — disable with `--no-restore` / `--no-save`). `open --sync` also runs `store sync` once the session is saved, so it reaches the remote without a second command.

A session ends when Chrome exits: close its window, or on macOS **quit** it (⌘Q), since closing the last window there leaves Chrome running. Ctrl-C in the terminal works too. Either way Chrome is asked to quit, not killed, so it writes cookies and storage to disk before the bundle is packed; only a browser that has not exited after 15 seconds is killed.

`open` only restores a bundle when there is **no local working copy**, since unpacking replaces the directory outright and the local copy may hold newer browsing. So after pulling a teammate's newer session for a profile you have already opened, `open` keeps yours and says so — run `profile pull <name>` to take theirs.

### How cookies move between machines

Copying the user-data-dir is not enough to move a login. Chrome encrypts each cookie value with a key that the operating system keeps for it: the login Keychain on macOS, libsecret or kwallet on Linux, DPAPI and app-bound encryption on Windows. That key is different on every machine. On a teammate's machine Chrome cannot decrypt the copied `Cookies` database, drops every cookie without an error, and the account opens signed out.

So a save also exports the cookies. After the session ends, hamzad starts the same Chrome once more on the profile for about a second: no window, no network (its traffic goes to a dead loopback proxy), and a DevTools port that only hamzad uses. It reads every cookie in plaintext with `Storage.getCookies` and writes them into the bundle as `.hamzad-cookies.json`. That file exists only inside the encrypted bundle and is never written to the working directory. A restore unpacks the bundle, deletes the copied cookie database, and writes the cookies back through the same kind of helper with `Storage.setCookies`, so Chrome encrypts them with this machine's key. `open`, `pull`, `push` and the TUI all do this.

This works across macOS, Linux and Windows, because Chrome does all the encryption itself. Your own session is still the clean launch with no DevTools port; the helper runs only before or after it. GoLogin does this differently: it ships a patched Chromium ("Orbita") that it launches with `--password-store=basic`, so the cookie key is the same on every machine and the raw `Cookies` file can move as it is. A stock Chrome cannot do that on macOS or Windows.

Limits:

- Only cookies are made portable. Saved passwords (`Login Data`) are encrypted the same way and do not decrypt on another machine.
- Session cookies (no expiry) end with the browser session, as they always do, so they are not in the export.
- Chrome caps a cookie's expiry at 400 days when it is written back.
- A site can still ask you to sign in again when the IP or device changes, and Google can bind a session to the device (Device Bound Session Credentials), which no copy can move. Use one machine at a time: two machines on one session often sign both out when the site rotates its tokens.
- A bundle saved by an older hamzad has no cookie export. It still restores in full on the machine that saved it.
- If the export fails (for example, no browser is found), the save continues without it and prints a warning.

### Importing and exporting cookies

`profile cookies` moves cookies between a profile and other tools.

```sh
hamzad profile cookies import work cookies.json          # add to the profile's cookies
hamzad profile cookies import work cookies.txt --replace # replace them
curl … | hamzad profile cookies import work -            # from standard input
hamzad profile cookies export work -f cdp -o state.json  # json | cdp | netscape
```

`import` detects the format: Cookie-Editor, EditThisCookie or GoLogin JSON; Puppeteer or Playwright JSON (including a Playwright `storageState` file); hamzad's own bundle export; or Netscape `cookies.txt` from curl, wget or yt-dlp. `export` writes `json` (the Cookie-Editor format, which most cookie tools import), `cdp` (for Puppeteer `page.setCookie` and Playwright `addCookies`) or `netscape`.

Both go through the profile's browser, the same way as a session save, so the profile must not be open while you run them. When a profile has no local copy yet, `export` reads the cookies from the shared bundle with no browser, and `import` restores the bundle first, so the shared session is not lost. After an import, run `profile push` to share the result.

An exported file contains live logins in plaintext. `-o` writes it with owner-only permissions; delete it when you are done.

### Adding a recipient after the fact

age fixes the recipient set at the moment a file is written, so a key added later cannot read anything already stored. A new teammate sees `profiles.toml` and `recipients.txt` — both plaintext — but every `profile open` fails at the decrypt. Adding the key is only half the job:

```sh
hamzad store recipients add 'ssh-ed25519 AAAA...teammate'
hamzad store reencrypt   # rewrite every secret and bundle to the current recipients
hamzad store sync -m "add teammate"
```

`store recipients add --reencrypt` does both in one step.

`reencrypt` needs your identity — it decrypts each artifact before writing it back — and it re-encrypts only what your key can read. Anything it cannot decrypt is listed as `FAILED` and left untouched, and the command exits non-zero, so a partial rotation cannot be mistaken for a complete one. Ask whoever can still read those profiles to run it on their machine.

Every bundle it rewrites is committed whole on the next sync, since age output is incompressible and git cannot delta it — the same size ceiling described above applies.

## Automation attach

`profile open --cdp-port <port>` fixes Chrome's remote-debugging port and prints the `webSocketDebuggerUrl`. Point Puppeteer (`connect`), Playwright (`connectOverCDP`), or Selenium (`debuggerAddress`) at it to automate the profile.

### Local API

`hamzad serve` runs a small HTTP API that starts and stops profiles and returns their DevTools endpoints, so a script can drive many profiles without a terminal for each. It does for hamzad what GoLogin's local API does for its SDKs.

```sh
hamzad serve                       # prints the address and a random token
HAMZAD_SERVE_TOKEN=… hamzad serve  # or a fixed token (also --token)
```

| Request | Does |
| --- | --- |
| `GET /v1/profiles` | every profile, with its session if it has one |
| `GET /v1/profiles/{name}` | one profile |
| `POST /v1/profiles/{name}/start` | launch it; answers when DevTools is up, with `ws_endpoint`, `http_endpoint` and `cdp_port` |
| `POST /v1/profiles/{name}/stop` | quit it; answers once the session is saved |
| `GET /v1/sessions` | every session this server has run |

The `start` body is optional: `{"start_url": "…", "auto_geo": true, "no_restore": true, "no_save": true, "cdp_port": 9222}`, the same as the `profile open` flags. Without `cdp_port` a free port is chosen. Starting a profile that is already running answers `409` with the running session, so you can reuse its endpoint.

```js
const res = await fetch("http://127.0.0.1:36912/v1/profiles/work/start", {
  method: "POST",
  headers: { Authorization: `Bearer ${process.env.HAMZAD_SERVE_TOKEN}` },
});
const { ws_endpoint } = await res.json();
const browser = await puppeteer.connect({ browserWSEndpoint: ws_endpoint });
// Playwright: await chromium.connectOverCDP(ws_endpoint)
```

Things to know:

- A session started through the API always has a DevTools port, so it has the CDP overrides and also the automation tells described above. The clean launch is for `profile open`.
- Sessions are restored and saved like any other, including the cookie export. Stopping the server (Ctrl-C) stops and saves every browser that it started.
- The API controls signed-in browsers, so it is locked down. It listens on loopback only and refuses any other address. Every request needs `Authorization: Bearer <token>`. It refuses a request whose `Host` header is not a loopback address, which blocks a web page that uses DNS rebinding to reach the port.
- Profile responses say whether a proxy is set, never what it is.

## What is spoofed, and how reliable it is

The default **clean** launch can only use process-level flags and environment. Everything else needs the CDP session that `--cdp-port` opens.

| Signal | Mechanism | Clean (default) | `--cdp-port` |
| --- | --- | --- | --- |
| Profile isolation | separate `--user-data-dir` | ✅ | ✅ |
| WebRTC IP leak | `--webrtc-ip-handling-policy` | ✅ | ✅ |
| Proxy (HTTP/SOCKS) + auth | `--proxy-server` + local auth relay | ✅ | ✅ |
| User-Agent | `--user-agent` / CDP `setUserAgentOverride` | ✅ | ✅ |
| Timezone | `TZ` env / CDP `setTimezoneOverride` | ✅ | ✅ (engine-native) |
| Window size | `--window-size` | ✅ | ✅ |
| Primary language | `--lang` | ✅ | ✅ |
| `navigator.platform` | CDP `setUserAgentOverride` platform hint | ❌ | ✅ |
| Accept-Language / locale | CDP `setUserAgentOverride` / `setLocaleOverride` | ❌ | ✅ |
| Geolocation | CDP `setGeolocationOverride` + permission grant | ❌ | ✅ |
| `screen.width/height` | injected JS (`setDeviceMetricsOverride` sizes the viewport, not `screen`) | ❌ | best-effort, detectable |
| `hardwareConcurrency` | CDP `setHardwareConcurrencyOverride` | ❌ | ✅ (engine-level, reaches Workers) |
| deviceMemory / languages | injected JS (`defineProperty`) | ❌ | best-effort, detectable |
| Canvas noise / WebGL vendor-renderer | injected JS hooks | ❌ | best-effort, detectable |

Mind the ❌ on `navigator.platform`: an operating-system preset sets the platform and the user-agent together, and a clean launch applies only the user-agent — so a Windows profile on a Linux host advertises a Windows UA with a Linux `navigator.platform`, which is a *sharper* signal than not spoofing at all. Use `--cdp-port` when the profile pins a platform, or leave the platform unset. `profile open` and the TUI both name the overrides they had to drop.

The default landing page reports every configured value beside the one the browser actually hands to page JavaScript, so a launch can be verified at a glance.

**`navigator.webdriver` is deliberately not patched.** Chrome reports it `false` here on its own — nothing passes `--enable-automation`, and an attached CDP session does not set the flag — so a `defineProperty` would replace an honest native getter with a JS one, and a JS getter is exactly what `Function.prototype.toString` exposes. This is not theoretical: with the patch, `accounts.google.com` refuses the browser with "This browser or app may not be secure"; without it, the same profile signs in normally. It is the one signal where spoofing is strictly worse than telling the truth.

For high-scrutiny targets the JS-patched signals need a patched Chromium (GoLogin's "Orbita"); the launch layer is designed so such a binary can be swapped in via `--chrome-path` without changing the CLI.

A profile with a proxy defaults to `disable_non_proxied_udp`, because WebRTC otherwise speaks STUN over UDP straight from the host's interfaces — around the proxy entirely — and hands any page the real address while every other signal insists otherwise. That mismatch is worse than not proxying at all. A profile without a proxy keeps Chrome's default so video calls still work; override either with `--webrtc-mode`. New profiles open with browserleaks.com/ip and /webrtc bookmarked, so the claim is one click from being checked.

Three known leaks remain, all stated plainly because they are the difference between "spoofed" and "unnoticed".

**Injected patches do not reach a Worker.** They run only in the page's main JS world, and a `Worker` gets a fresh one — so `navigator.deviceMemory` and `navigator.languages` there report the host's real values while the page claims otherwise. The mismatch is the tell, not the number. `hardwareConcurrency` used to leak the same way and no longer does: it moved to CDP's `setHardwareConcurrencyOverride`, which the engine applies everywhere. There is no equivalent for the others, so closing them needs either a patched Chromium or intercepting worker construction, both of which carry their own tells.

**`navigator.platform` does not survive a second CDP client.** It is carried by `Emulation.setUserAgentOverride`, which Chrome scopes to the DevTools session that set it — and it resets when *another* session attaches and detaches. So a Puppeteer or Playwright client connecting to `--cdp-port` silently strips the platform override on disconnect, leaving a Windows user-agent on a host-platform `navigator.platform`. Re-applying on session change is the fix; it is not implemented yet.

**Confirmation-page blind spot.** The default landing page is a `data:` URL, which is an opaque, non-secure origin. Geolocation refuses to report there (`Only secure origins are allowed`), so the page cannot verify the one signal it most wants to, and `navigator.userAgentData` is undefined so the platform row falls back to `navigator.platform`. Serving it from loopback instead would fix both.

Canvas noise is a single fixed pattern rather than a per-profile one: it hides a profile's canvas from the host's own fingerprint, but two noised profiles still hash alike, so it does not defeat cross-profile linking.

## Configuration

hamzad reads `config.toml` from its config root (`~/.config/hamzad` on Linux, `~/Library/Application Support/hamzad` on macOS) when it exists, or the file named by `--config` / `HAMZAD_CONFIG`. See [`configs/config.example.toml`](configs/config.example.toml). Every key is also settable via `HAMZAD_`-prefixed environment variables (e.g. `HAMZAD_STORE_DIR`).

## License

GPL-3.0-or-later. See [LICENSE](LICENSE).

Relicensed from Apache-2.0 in v0.3.0, when the proxy layer moved onto [sing-box](https://github.com/SagerNet/sing-box). sing-box is GPL-3.0-or-later, and linking it makes the combined work GPL-3.0 — so the project follows. In practice: you may use, modify and redistribute this freely, but a distributed binary must come with its source under the same terms.
