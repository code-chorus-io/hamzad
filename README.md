# koochooloologin

A small, GoLogin-style **anti-detect browser manager** on the command line. Each *profile* is an isolated Chrome identity with its own proxy, timezone, geolocation, and fingerprint. Profiles are stored in a git repository so you can **share** them with a teammate by pushing to a remote — while the browsing data (cookies, history) stays on your machine.

It drives an ordinary Chrome/Chromium — no patched browser. By default a profile launches **clean**: a plain child process with no DevTools (CDP) session and no remote-debugging port, so none of the automation tells that block sites like Google sign-in are present. The tradeoff is that overrides needing a live CDP session — `navigator.platform`, Accept-Language, geolocation, screen metrics, and every JS-level fingerprint patch — are *not* applied. Pass `--cdp-port` to get them. See [what is spoofed](#what-is-spoofed-and-how-reliable-it-is) for the per-signal split; `profile open` prints which overrides it had to drop.

## Install

Prebuilt static binaries for Linux, macOS, and Windows (amd64 and arm64) are attached to every [release](https://github.com/1995parham/koochooloologin/releases). Download the archive for your platform, check it against the release's `checksums.txt`, and put `koochooloologin` on your `PATH`.

```sh
go install github.com/1995parham/koochooloologin/cmd/koochooloologin@latest
# or, from a checkout:
just build      # produces ./koochooloologin
just snapshot   # builds the release archives into ./dist, publishing nothing
```

You also need a browser. Either bring your own — auto-detected from `PATH`, or set `--chrome-path` / `chrome_path` — or let koochooloologin fetch one:

```sh
koochooloologin browser install stable   # ~190 MB, Chrome for Testing
koochooloologin browser list
koochooloologin browser path             # which binary a launch would use
```

Downloads go to `~/.cache/koochooloologin/browsers/<version>/` and never auto-update. Nothing is fetched implicitly: `profile open` will not pull a browser mid-command, it tells you which `browser install` to run.

Pinning a version with `chrome_version` is worth doing. A profile's user-agent is only convincing while it matches the engine actually rendering the page, and a teammate resuming a shared profile on their own Chrome is running a subtly different identity than the one you shared.

**Why Chrome for Testing and not Chromium.** Chromium's openness buys nothing without recompiling it, and a stock Chromium binary is *more* identifiable than Chrome: no proprietary codecs (H.264/AAC), `"Chromium"` rather than `"Google Chrome"` in its user-agent client-hint brands, and no Widevine — three signals a fingerprinter reads for free. Chrome for Testing is the same browser Google ships to users, just pinned and packaged for automation. If you want engine-level spoofing, the answer is a *patched* Chromium you build yourself (GoLogin's "Orbita" and friends), pointed at with `--chrome-path`.

On Linux the archive carries no system libraries, so a minimal desktop or container needs a handful installed separately. `browser install` runs the binary once and tells you exactly which are missing rather than leaving it to fail at launch. There is no `linux-arm64` build — Google publishes none — so on that platform bring your own browser.

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

# the geolocation saved above needs a CDP session to take effect; the timezone
# applies either way. --cdp-port also exposes the port for automation to attach.
koochooloologin profile open work --cdp-port 9222

# ...or derive geo for this session only, without saving it to the profile
koochooloologin profile open work --auto-geo --cdp-port 9222

koochooloologin profile list
koochooloologin profile show work
koochooloologin profile remove work --purge
```

`profile geo` and `--auto-geo` ask [ipwho.is](https://ipwho.is) over HTTPS, from behind the profile's own proxy. TLS matters here: the question travels through the very proxy being measured, so over plain HTTP its operator could choose the answer the profile then pins. The lookup routes through `http`, `https`, and `socks5` proxies; `socks4` is refused up front because Go's HTTP client cannot dial it — Chrome still uses such a proxy fine, so set the timezone by hand.

## Sharing profiles (git + age encryption)

The store is a git repository. Secrets and session state are **encrypted with [age](https://age-encryption.org)** before they are committed, using your team's public keys — **SSH keys work directly as age recipients**, so there is nothing new to generate.

| File | Committed? | Contents |
| --- | --- | --- |
| `profiles.toml` | yes, plaintext | non-secret config: fingerprint, timezone, notes |
| `recipients.txt` | yes, plaintext | age/SSH **public** keys allowed to decrypt |
| `secrets/<name>.age` | yes, encrypted | the profile's proxy (incl. credentials) |
| `data/<name>.tar.age` | yes, encrypted | full Chrome user-data-dir (cookies, storage) |
| `data/<name>/` | **gitignored** | the local, unencrypted working copy |

Encrypting needs only the **public** recipients, so `add`/`push` never touch your private key. Only **decrypting** — `open`, `pull`, `show --reveal` — uses your identity (`~/.ssh/id_ed25519` by default; override with `--identity`).

```sh
# init seeds recipients.txt with your ~/.ssh/id_ed25519.pub
koochooloologin store init --remote git@github.com:you/profiles.git

# add a teammate so they can decrypt (their SSH public key, or a file of keys)
koochooloologin store recipients add 'ssh-ed25519 AAAA...teammate'
koochooloologin store recipients list

koochooloologin store sync -m "share work profile"   # commit + pull --rebase + push
koochooloologin store status
```

A teammate clones the repo, runs `store sync`, then `profile open work` — their copy decrypts the proxy and the session bundle and resumes the account.

`profile push` / `profile pull` explicitly re-encrypt / restore a session bundle; `open` does both automatically (restore before launch, save on exit — disable with `--no-restore` / `--no-save`).

`open` only restores a bundle when there is **no local working copy**, since unpacking replaces the directory outright and the local copy may hold newer browsing. So after pulling a teammate's newer session for a profile you have already opened, `open` keeps yours and says so — run `profile pull <name>` to take theirs.

> After adding new recipients, re-encrypt existing secrets so they can read them (`profile add --proxy …` again, or `profile push`). age can only encrypt to keys known at encryption time.

## Automation attach

`profile open --cdp-port <port>` fixes Chrome's remote-debugging port and prints the `webSocketDebuggerUrl`. Point Puppeteer (`connect`), Playwright (`connectOverCDP`), or Selenium (`debuggerAddress`) at it to automate the profile.

## What is spoofed, and how reliable it is

The default **clean** launch can only use process-level flags and environment. Everything else needs the CDP session that `--cdp-port` opens.

| Signal | Mechanism | Clean (default) | `--cdp-port` |
| --- | --- | --- | --- |
| Profile isolation | separate `--user-data-dir` | ✅ | ✅ |
| Proxy (HTTP/SOCKS) + auth | `--proxy-server` + local auth relay | ✅ | ✅ |
| User-Agent | `--user-agent` / CDP `setUserAgentOverride` | ✅ | ✅ |
| Timezone | `TZ` env / CDP `setTimezoneOverride` | ✅ | ✅ (engine-native) |
| Window size | `--window-size` | ✅ | ✅ |
| Primary language | `--lang` | ✅ | ✅ |
| `navigator.platform` | CDP `setUserAgentOverride` platform hint | ❌ | ✅ |
| Accept-Language / locale | CDP `setUserAgentOverride` / `setLocaleOverride` | ❌ | ✅ |
| Geolocation | CDP `setGeolocationOverride` + permission grant | ❌ | ✅ |
| `screen.width/height` | CDP `setDeviceMetricsOverride` + injected JS | ❌ | ✅ |
| hardwareConcurrency / deviceMemory / languages | injected JS (`defineProperty`) | ❌ | best-effort, detectable |
| Canvas noise / WebGL vendor-renderer | injected JS hooks | ❌ | best-effort, detectable |

Mind the ❌ on `navigator.platform`: an operating-system preset sets the platform and the user-agent together, and a clean launch applies only the user-agent — so a Windows profile on a Linux host advertises a Windows UA with a Linux `navigator.platform`, which is a *sharper* signal than not spoofing at all. Use `--cdp-port` when the profile pins a platform, or leave the platform unset. `profile open` and the TUI both name the overrides they had to drop.

The default landing page reports every configured value beside the one the browser actually hands to page JavaScript, so a launch can be verified at a glance.

For high-scrutiny targets the JS-patched signals need a patched Chromium (GoLogin's "Orbita"); the launch layer is designed so such a binary can be swapped in via `--chrome-path` without changing the CLI.

Two known leaks in the current spoofing, both inherent to patching from JavaScript rather than in the engine. The injected patches run only in the page's main world, so reading `navigator.hardwareConcurrency` or `deviceMemory` inside a `Worker` returns the real host values — a standard check. And the `--cdp-port` path enables the CDP `Runtime` domain, which is itself a signal some anti-bot services probe for. The CDP-native overrides (user-agent, timezone, geolocation, device metrics) are engine-level and have neither problem.

Canvas noise is a single fixed pattern rather than a per-profile one: it hides a profile's canvas from the host's own fingerprint, but two noised profiles still hash alike, so it does not defeat cross-profile linking.

## Configuration

See [`configs/config.example.toml`](configs/config.example.toml). Every key is also settable via `KEL_`-prefixed environment variables (e.g. `KEL_STORE_DIR`).

## License

Apache-2.0. See [LICENSE](LICENSE).
