package chrome

// expectedPlaceholder is the token in confirmHTML that confirmPageURL swaps for
// the injected EXPECTED JSON. A sentinel (not a %-verb) is used because the
// page's CSS carries literal percent signs.
const expectedPlaceholder = "__EXPECTED_JSON__"

// confirmHTML is the confirmation page template. It carries a single
// __EXPECTED_JSON__ slot for the injected EXPECTED JSON (see expectedConfig).
// The script reads each
// value the browser actually reports and renders it beside the configured one,
// marking configured fields that match (ok), differ (bad), or were left at the
// browser default (informational). It is fully self-contained so it can be
// served from a data: URL with no network or file dependency.
const confirmHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Profile configuration</title>
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; }
  body {
    margin: 0; padding: 2.5rem 1.25rem;
    font: 15px/1.5 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    background: #0f1115; color: #e6e8eb;
    display: flex; justify-content: center;
  }
  main { width: 100%; max-width: 760px; }
  h1 { font-size: 1.4rem; margin: 0 0 .25rem; }
  p.sub { margin: 0 0 1.75rem; color: #9aa0a6; }
  table { width: 100%; border-collapse: collapse; }
  th, td { text-align: left; padding: .6rem .7rem; vertical-align: top; }
  th { font-size: .72rem; text-transform: uppercase; letter-spacing: .05em; color: #9aa0a6; border-bottom: 1px solid #2a2e37; }
  td { border-bottom: 1px solid #1a1d24; }
  td.field { color: #9aa0a6; white-space: nowrap; }
  td.val { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: .85rem; word-break: break-word; }
  .status { width: 1.6rem; text-align: center; font-weight: 700; }
  .ok  { color: #34d399; }
  .bad { color: #f87171; }
  .na  { color: #6b7280; }
  .legend { margin-top: 1.5rem; font-size: .8rem; color: #9aa0a6; }
  .legend span { margin-right: 1.1rem; }
</style>
</head>
<body>
<main>
  <h1>Profile configuration</h1>
  <p class="sub">Configured values beside what this browser actually reports.</p>
  <table>
    <thead>
      <tr><th class="status"></th><th>Signal</th><th>Expected</th><th>Actual</th></tr>
    </thead>
    <tbody id="rows"></tbody>
  </table>
  <p class="legend">
    <span class="ok">✓ matches</span>
    <span class="bad">✗ differs</span>
    <span class="na">• browser default</span>
  </p>
</main>
<script>
  const EXPECTED = __EXPECTED_JSON__;

  const glParam = (p) => {
    try {
      const c = document.createElement("canvas");
      const gl = c.getContext("webgl") || c.getContext("experimental-webgl");
      if (!gl) return "";
      const ext = gl.getExtension("WEBGL_debug_renderer_info");
      if (!ext) return "";
      return String(gl.getParameter(p === "vendor" ? ext.UNMASKED_VENDOR_WEBGL : ext.UNMASKED_RENDERER_WEBGL));
    } catch (e) { return ""; }
  };

  const rows = [
    { key: "userAgent",           label: "User agent",     read: () => navigator.userAgent },
    { key: "platform",            label: "Platform",       read: () => (navigator.userAgentData && navigator.userAgentData.platform) || navigator.platform },
    { key: "timezone",            label: "Timezone",       read: () => Intl.DateTimeFormat().resolvedOptions().timeZone },
    { key: "languages",           label: "Languages",      read: () => (navigator.languages || []).join(", ") },
    { key: "screen",              label: "Screen",         read: () => screen.width + " × " + screen.height },
    { key: "hardwareConcurrency", label: "CPU cores",      read: () => String(navigator.hardwareConcurrency) },
    { key: "deviceMemory",        label: "Device memory",  read: () => navigator.deviceMemory != null ? String(navigator.deviceMemory) : "" },
    { key: "webglVendor",         label: "WebGL vendor",   read: () => glParam("vendor") },
    { key: "webglRenderer",       label: "WebGL renderer", read: () => glParam("renderer") },
    { key: "webdriver",           label: "WebDriver",      read: () => String(navigator.webdriver) },
    { key: "geolocation",         label: "Geolocation",    read: () => "…", async: geoRead },
    { key: "proxy",               label: "Exit IP",        read: () => "…", async: ipRead },
  ];

  const norm = (s) => String(s == null ? "" : s).trim().toLowerCase().replace(/\s+/g, " ");

  function classify(expected, actual) {
    if (!expected) return "na";
    return norm(expected) === norm(actual) ? "ok" : "bad";
  }
  function mark(cls) { return cls === "ok" ? "✓" : cls === "bad" ? "✗" : "•"; }

  const tbody = document.getElementById("rows");

  for (const r of rows) {
    const expected = EXPECTED[r.key] || "";
    const actual = r.read();
    const cls = classify(expected, actual);

    const tr = document.createElement("tr");
    tr.innerHTML =
      '<td class="status ' + cls + '" data-cell="status">' + mark(cls) + '</td>' +
      '<td class="field"></td>' +
      '<td class="val" data-cell="expected"></td>' +
      '<td class="val" data-cell="actual"></td>';
    tr.querySelector(".field").textContent = r.label;
    tr.querySelector('[data-cell="expected"]').textContent = expected || "—";
    tr.querySelector('[data-cell="actual"]').textContent = actual;
    tbody.appendChild(tr);

    if (r.async) r.async(tr, expected);
  }

  function update(tr, expected, actual) {
    const cls = classify(expected, actual);
    const st = tr.querySelector('[data-cell="status"]');
    st.className = "status " + cls;
    st.textContent = mark(cls);
    tr.querySelector('[data-cell="actual"]').textContent = actual;
  }

  function geoRead(tr, expected) {
    if (!navigator.geolocation) { update(tr, expected, "unavailable"); return; }
    navigator.geolocation.getCurrentPosition(
      (pos) => update(tr, expected, pos.coords.latitude.toFixed(5) + ", " + pos.coords.longitude.toFixed(5)),
      (err) => update(tr, expected, "error: " + err.message),
      { timeout: 5000 },
    );
  }

  function ipRead(tr, expected) {
    // Goes out through the profile's proxy, so it confirms the exit IP. Best
    // effort: the field is informational and never fails the check.
    fetch("https://api.ipify.org?format=json")
      .then((r) => r.json())
      .then((d) => update(tr, expected, d.ip))
      .catch(() => update(tr, expected, "unavailable"));
  }
</script>
</body>
</html>`
