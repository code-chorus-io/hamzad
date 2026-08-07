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
| `data/<name>.tar.age` | yes, encrypted | full Chrome user-data-dir (cookies, storage) |
| `data/<name>/` | **gitignored** | the local, unencrypted working copy |

Encrypting needs only the **public** recipients, so `add`/`push` never touch your private key. Only **decrypting** — `open`, `pull`, `show --reveal` — uses your identity (`~/.ssh/id_ed25519` by default; override with `--identity`).

```sh
# init seeds recipients.txt with your ~/.ssh/id_ed25519.pub
hamzad store init --remote git@github.com:you/profiles.git

# add a teammate so they can decrypt (their SSH public key, or a file of keys)
hamzad store recipients add 'ssh-ed25519 AAAA...teammate'
hamzad store recipients list

hamzad store sync -m "share work profile"   # commit + pull --rebase + push
hamzad store status
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

See [`configs/config.example.toml`](configs/config.example.toml). Every key is also settable via `HAMZAD_`-prefixed environment variables (e.g. `HAMZAD_STORE_DIR`).

## License

GPL-3.0-or-later. See [LICENSE](LICENSE).

Relicensed from Apache-2.0 in v0.3.0, when the proxy layer moved onto [sing-box](https://github.com/SagerNet/sing-box). sing-box is GPL-3.0-or-later, and linking it makes the combined work GPL-3.0 — so the project follows. In practice: you may use, modify and redistribute this freely, but a distributed binary must come with its source under the same terms.
