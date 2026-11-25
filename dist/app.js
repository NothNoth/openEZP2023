"use strict";

const $ = (id) => document.getElementById(id);
const hex = (n, w = 6) => "0x" + n.toString(16).toUpperCase().padStart(w, "0");

const state = {
  chips: [],
  chip: null,        // selected chip object
  connected: false,
  busy: false,
  buffer: null,      // Uint8Array
  bufferName: "",
};

// ---------- formatting ----------
function size(n) {
  if (n >= 1 << 20) return `${n / (1 << 20)} MB`;
  if (n >= 1024) return `${n / 1024} KB`;
  return `${n} bytes`;
}

function log(msg, kind = "") {
  const li = document.createElement("li");
  li.className = kind;
  const t = document.createElement("time");
  t.textContent = new Date().toLocaleTimeString();
  const s = document.createElement("span");
  s.textContent = msg;
  li.append(t, s);
  $("log").prepend(li);
}

// ---------- chip picker ----------
const LIST_LIMIT = 300;

function renderList() {
  const q = $("search").value.trim().toLowerCase();
  const terms = q.split(/\s+/).filter(Boolean);
  const hits = state.chips.filter((c) => {
    const hay = `${c.vendor} ${c.model} ${c.jedec ?? ""} ${c.type}`.toLowerCase();
    return terms.every((t) => hay.includes(t));
  });
  const ul = $("chiplist");
  ul.replaceChildren();
  for (const c of hits.slice(0, LIST_LIMIT)) {
    const li = document.createElement("li");
    li.setAttribute("role", "option");
    li.dataset.index = c.index;
    li.setAttribute("aria-selected", String(state.chip?.index === c.index));
    const name = document.createElement("span");
    name.textContent = `${c.vendor} ${c.model}`;
    if (c.needs18V) {
      const b = document.createElement("span");
      b.className = "badge";
      b.textContent = "1.8 V";
      name.append(b);
    }
    const meta = document.createElement("small");
    meta.textContent = size(c.size);
    li.append(name, meta);
    ul.append(li);
  }
  $("more").textContent =
    hits.length === 0 ? "No chip matches. Try the vendor name or a JEDEC ID like EF4018."
    : hits.length > LIST_LIMIT ? `Showing ${LIST_LIMIT} of ${hits.length}. Refine the search to see more.`
    : `${hits.length} chip${hits.length > 1 ? "s" : ""}`;
}

function selectChip(c, { scroll = false } = {}) {
  state.chip = c;
  $("chip-name").textContent = `${c.vendor} ${c.model}`;
  const rows = [
    ["Type", c.protocol],
    ["Size", `${size(c.size)} (${c.size.toLocaleString()} bytes)`],
    ["Page", `${c.page} bytes`],
    ["Voltage", c.needs18V ? "1.8 V (adapter required)" : c.voltage],
  ];
  if (c.jedec) rows.push(["JEDEC ID", c.jedec]);
  const dl = $("chip-details");
  dl.replaceChildren();
  for (const [k, v] of rows) {
    const dt = document.createElement("dt"); dt.textContent = k;
    const dd = document.createElement("dd"); dd.textContent = v;
    if (k === "JEDEC ID") dd.className = "mono";
    dl.append(dt, dd);
  }
  $("map-end").textContent = hex(Math.max(c.size - 1, 0));
  $("chip-warn").hidden = !c.needs18V;
  $("ops-warn").hidden = !c.needs18V;
  for (const li of $("chiplist").children) {
    const sel = Number(li.dataset.index) === c.index;
    li.setAttribute("aria-selected", String(sel));
    if (sel && scroll) li.scrollIntoView({ block: "nearest" });
  }
  try { localStorage.setItem("ezp.chip", String(c.index)); } catch {}
  refresh();
}

// ---------- memory map ----------
function mapProgress(done, total, label) {
  const pct = total ? (done / total) * 100 : 0;
  $("map").className = "map-bar";
  $("map-fill").style.width = `${pct}%`;
  // For data operations "done" is an address; for erase it is a step count.
  $("map-now").textContent = label === "erase" ? `${Math.round(pct)} %` : hex(Math.max(done - 1, 0));
}

function mapResult(kind, caption) {
  $("map").className = `map-bar ${kind}`;
  if (kind) $("map-fill").style.width = "100%";
  $("map-now").textContent = "";
  $("caption").textContent = caption;
}

function mapMismatch(offset, total, caption) {
  $("map").className = "map-bar err";
  $("map-fill").style.width = `${(offset / total) * 100}%`;
  $("map-now").textContent = hex(offset);
  $("caption").textContent = caption;
}

// ---------- hex view (virtualised) ----------
const LINE_H = 24; // must match #hex line-height in px (1.5rem at 16px)

function renderHex() {
  const box = $("hex");
  const spacer = $("hex-spacer");
  const buf = state.buffer;
  if (!buf) {
    spacer.style.height = "auto";
    spacer.innerHTML = '<pre class="empty">Read a chip or open a file to see its contents here.</pre>';
    return;
  }
  const lines = Math.ceil(buf.length / 16);
  spacer.style.height = `${lines * LINE_H}px`;
  const first = Math.max(0, Math.floor(box.scrollTop / LINE_H) - 5);
  const count = Math.ceil(box.clientHeight / LINE_H) + 10;
  const aw = buf.length > 0x1000000 ? 8 : 6;
  let html = "";
  for (let l = first; l < Math.min(lines, first + count); l++) {
    const off = l * 16;
    let hx = "", asc = "";
    for (let i = 0; i < 16; i++) {
      if (off + i >= buf.length) { hx += "   "; continue; }
      const b = buf[off + i];
      const h = b.toString(16).toUpperCase().padStart(2, "0");
      hx += (b === 0xff ? `<span class="ff">${h}</span>` : h) + (i === 7 ? "  " : " ");
      asc += b >= 0x20 && b < 0x7f ? String.fromCharCode(b).replace(/[&<>]/g, (ch) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;" }[ch])) : ".";
    }
    html += `<span class="addr">${off.toString(16).toUpperCase().padStart(aw, "0")}</span>  ${hx} ${asc}\n`;
  }
  spacer.innerHTML = `<pre style="top:${first * LINE_H}px">${html}</pre>`;
}

function setBuffer(bytes, name) {
  state.buffer = bytes;
  state.bufferName = name;
  $("buf-meta").textContent = bytes ? `${name}, ${size(bytes.length)} (${bytes.length.toLocaleString()} bytes)` : "Empty";
  $("hex").scrollTop = 0;
  renderHex();
  refresh();
}

// ---------- UI state ----------
function refresh() {
  const ready = state.connected && !state.busy;
  for (const b of document.querySelectorAll("[data-op]")) {
    const op = b.dataset.op;
    const needsChip = op !== "detect";
    const needsBuf = op === "write" || op === "verify";
    b.disabled = !ready || (needsChip && !state.chip) || (needsBuf && !state.buffer);
  }
  $("cancel").disabled = !state.busy;
  $("save").disabled = !state.buffer || state.busy;
  $("open").disabled = state.busy;
  $("connect").hidden = state.connected;
  $("demo").hidden = state.connected;
  $("disconnect").hidden = !state.connected;
  $("disconnect").disabled = state.busy;
}

function setConnected(info) {
  state.connected = !!info;
  const st = $("status");
  st.classList.toggle("on", state.connected);
  if (!info) {
    st.textContent = "Not connected";
  } else if (info.kind === "demo") {
    st.textContent = `${info.product} (simulated)`;
  } else {
    st.textContent = `Connected: ${info.product || "EZP2023+"}, revision ${info.revision.split(" ")[0]}`;
    if (!info.knownProduct) log(`Unexpected product name "${info.product}". Assuming revision A; operations may fail.`, "err");
    if (info.manufacturer && info.manufacturer !== "www.zhifengsoft.com") log(`Unexpected manufacturer "${info.manufacturer}".`, "err");
  }
  refresh();
}

// ---------- operations ----------
async function busy(label, fn) {
  state.busy = true;
  refresh();
  try {
    return await fn();
  } catch (e) {
    const msg = e?.message || String(e);
    mapResult("err", `${label} failed: ${msg}`);
    log(`${label} failed: ${msg}`, "err");
    return undefined;
  } finally {
    state.busy = false;
    refresh();
  }
}

const progressFor = (label) => (done, total) => mapProgress(done, total, label);

async function opDetect() {
  return busy("Detect", async () => {
    $("caption").textContent = "Detecting…";
    const r = await ezp.detect(state.chip ? state.chip.index : undefined);
    if (!r.signatureOK) {
      // Seen as 9A7336BD on real hardware. A different value means the reply
      // is garbled or out of step, or the hardware revision was misidentified.
      log(`Unexpected reply from the programmer (signature ${r.signature}, expected 9A7336BD). Results may be wrong; unplug and reconnect the programmer, and report this if it persists.`, "err");
    }
    if (r.bus === 0) {
      mapResult("err", "No chip answered. Check the chip orientation in the socket or clip, and that the right adapter is used.");
      log("Detect: no chip answered", "err");
      return;
    }
    if (r.jedec) {
      const matches = r.matches.map((i) => state.chips.find((c) => c.index === i));
      if (matches.length === 0) {
        mapResult("err", `SPI chip found with JEDEC ID ${r.jedec}, which is not in the chip list.`);
        log(`Detect: unknown JEDEC ID ${r.jedec}`, "err");
        return;
      }
      if (!state.chip || state.chip.jedec !== r.jedec) selectChip(matches[0], { scroll: true });
      const names = matches.map((c) => `${c.vendor} ${c.model}`).join(", ");
      mapResult("ok", `Found JEDEC ID ${r.jedec}: ${names}.`);
      log(`Detect: ${r.jedec} (${names})`, "ok");
      if (matches.some((c) => c.needs18V)) {
        $("caption").textContent += " This is a 1.8 V chip: use the 1.8 V adapter.";
        log(`${names}: 1.8 V chip, use the 1.8 V adapter`, "err");
      }
    } else {
      mapResult("ok", `A ${r.busName} chip answered. EEPROMs have no ID, so choose the exact model in the list.`);
      log(`Detect: ${r.busName} chip present`, "ok");
    }
  });
}

async function opRead() {
  const c = state.chip;
  return busy("Read", async () => {
    $("caption").textContent = `Reading ${c.vendor} ${c.model}…`;
    const t0 = performance.now();
    const data = await ezp.read(c.index, progressFor("read"));
    const s = ((performance.now() - t0) / 1000).toFixed(1);
    setBuffer(data, `${c.model}.bin`);
    mapResult("ok", `Read ${size(data.length)} in ${s} s. Save it before writing anything.`);
    log(`Read ${c.model}: ${data.length.toLocaleString()} bytes in ${s} s`, "ok");
  });
}

async function eraseStep(c) {
  $("caption").textContent = `Erasing ${c.vendor} ${c.model}…`;
  await ezp.erase(c.index, progressFor("erase"));
  log(`Erased ${c.model}`, "ok");
}

async function opErase() {
  const c = state.chip;
  if (!confirm(`Erase ${c.vendor} ${c.model}? Its whole contents will be lost.`)) return;
  return busy("Erase", async () => {
    await eraseStep(c);
    mapResult("ok", "Erased. Run a blank check to confirm.");
  });
}

async function opBlank() {
  const c = state.chip;
  return busy("Blank check", async () => {
    $("caption").textContent = "Checking that every byte is 0xFF…";
    const r = await ezp.blankCheck(c.index, progressFor("read"));
    if (r.ok) {
      mapResult("ok", "The chip is blank.");
      log(`Blank check ${c.model}: blank`, "ok");
    } else {
      mapMismatch(r.offset, c.size, `Not blank: first programmed byte at ${hex(r.offset)}.`);
      log(`Blank check ${c.model}: data at ${hex(r.offset)}`, "err");
    }
  });
}

async function verifyStep(c) {
  $("caption").textContent = "Verifying…";
  const r = await ezp.verify(c.index, state.buffer, progressFor("read"));
  if (r.ok) {
    mapResult("ok", `The chip matches ${state.bufferName}.`);
    log(`Verify ${c.model}: match`, "ok");
  } else {
    mapMismatch(r.offset, c.size, `Mismatch at ${hex(r.offset)}: the chip differs from ${state.bufferName}.`);
    log(`Verify ${c.model}: mismatch at ${hex(r.offset)}`, "err");
  }
  return r.ok;
}

async function opWrite() {
  const c = state.chip;
  const buf = state.buffer;
  if (buf.length > c.size) {
    mapResult("err", `${state.bufferName} is ${size(buf.length)}, larger than the ${size(c.size)} chip. Check the selected model.`);
    return;
  }
  const doErase = $("erase-first").checked;
  const doVerify = $("verify-after").checked;
  let msg = `Write ${state.bufferName} to ${c.vendor} ${c.model}?`;
  if (doErase) msg += " The chip will be erased first.";
  if (buf.length < c.size) msg += `\n\nThe file is smaller than the chip (${size(buf.length)} of ${size(c.size)}).`;
  if (!confirm(msg)) return;
  return busy("Write", async () => {
    if (doErase) await eraseStep(c);
    $("caption").textContent = `Writing ${state.bufferName}…`;
    await ezp.write(c.index, buf, progressFor("write"));
    log(`Wrote ${state.bufferName} to ${c.model}`, "ok");
    if (doVerify) await verifyStep(c);
    else mapResult("ok", "Written. Run a verify to confirm.");
  });
}

async function opVerify() {
  return busy("Verify", () => verifyStep(state.chip));
}

const ops = { detect: opDetect, read: opRead, erase: opErase, blank: opBlank, write: opWrite, verify: opVerify };

// ---------- wiring ----------
function wire() {
  $("search").addEventListener("input", renderList);
  $("chiplist").addEventListener("click", (e) => {
    const li = e.target.closest("li[data-index]");
    if (li) selectChip(state.chips.find((c) => c.index === Number(li.dataset.index)));
  });
  $("chiplist").addEventListener("keydown", (e) => {
    if (!["ArrowDown", "ArrowUp"].includes(e.key)) return;
    e.preventDefault();
    const items = [...$("chiplist").children];
    const cur = items.findIndex((li) => li.getAttribute("aria-selected") === "true");
    const next = items[Math.min(items.length - 1, Math.max(0, cur + (e.key === "ArrowDown" ? 1 : -1)))];
    if (next) selectChip(state.chips.find((c) => c.index === Number(next.dataset.index)), { scroll: true });
  });

  ezp.clocks.forEach((name, i) => $("clock").add(new Option(name, i)));
  $("clock").addEventListener("change", () => ezp.setClock(Number($("clock").value)));
  $("fastread").addEventListener("change", () => ezp.setReadChunk($("fastread").checked ? 65536 : 0));

  for (const b of document.querySelectorAll("[data-op]")) b.addEventListener("click", () => ops[b.dataset.op]());
  $("cancel").addEventListener("click", () => { ezp.cancel(); log("Cancel requested"); });

  // ezp.connect() must run inside the click handler (user gesture).
  $("connect").addEventListener("click", async () => {
    try {
      setConnected(await ezp.connect());
      log("Programmer connected", "ok");
      $("caption").textContent = "Connected. Run Detect to identify the chip.";
    } catch (e) {
      if (e?.message?.includes("No device selected")) return;
      log(`Connection failed: ${e.message}`, "err");
      $("caption").textContent = `Connection failed: ${e.message}`;
    }
  });
  $("demo").addEventListener("click", async () => {
    const c = state.chip || state.chips.find((x) => x.model === "W25Q64");
    setConnected(await ezp.connectDemo(c.index));
    if (!state.chip) selectChip(c, { scroll: true });
    log(`Demo mode with a simulated ${c.vendor} ${c.model}`);
    $("caption").textContent = "Demo mode: operations run against a simulated chip.";
  });
  $("disconnect").addEventListener("click", async () => {
    await ezp.disconnect();
    setConnected(null);
    log("Disconnected");
  });

  $("open").addEventListener("click", () => $("file").click());
  $("file").addEventListener("change", async () => {
    const f = $("file").files[0];
    if (!f) return;
    setBuffer(new Uint8Array(await f.arrayBuffer()), f.name);
    log(`Opened ${f.name} (${f.size.toLocaleString()} bytes)`);
    if (state.chip && f.size !== state.chip.size) {
      log(`${f.name} is ${size(f.size)}; the selected chip is ${size(state.chip.size)}.`, "err");
    }
    $("file").value = "";
  });
  $("save").addEventListener("click", () => {
    const url = URL.createObjectURL(new Blob([state.buffer], { type: "application/octet-stream" }));
    const a = Object.assign(document.createElement("a"), { href: url, download: state.bufferName || "dump.bin" });
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  });

  $("hex").addEventListener("scroll", () => requestAnimationFrame(renderHex));
  window.addEventListener("resize", renderHex);

  navigator.usb?.addEventListener("disconnect", () => {
    if (state.connected && !$("status").textContent.includes("simulated")) {
      setConnected(null);
      log("Programmer unplugged", "err");
    }
  });
}

async function start() {
  state.chips = [...ezp.chips].sort((a, b) =>
    a.vendor.localeCompare(b.vendor) || a.model.localeCompare(b.model, undefined, { numeric: true }));
  wire();
  renderList();
  renderHex();
  let saved = null;
  try { saved = localStorage.getItem("ezp.chip"); } catch {}
  const c = saved && state.chips.find((x) => x.index === Number(saved));
  if (c) selectChip(c, { scroll: true });

  $("connect").disabled = !ezp.supported();
  $("demo").disabled = false;
  setConnected(null);
  if (!window.isSecureContext) {
    $("caption").textContent = "WebUSB needs HTTPS: open this page with an https:// address. The demo works without it.";
  } else if (!ezp.supported()) {
    $("caption").textContent = "This browser has no WebUSB. Use Chrome, Edge or Opera, or try the demo.";
  } else {
    try {
      const info = await ezp.reconnect();
      if (info) { setConnected(info); log("Reconnected to the programmer", "ok"); }
    } catch (e) { log(`Reconnect failed: ${e.message}`, "err"); }
  }
}

// ---------- boot ----------
const WASM_URL = "ezp.wasm?v=ac04ceae54";

// instantiateStreaming needs the server to send "application/wasm"; many
// shared hosts don't, so fall back to a plain download in that case.
async function loadWasm(go) {
  const resp = await fetch(WASM_URL);
  if (!resp.ok) throw new Error(`the server answered ${resp.status} for ${WASM_URL}`);
  const type = resp.headers.get("content-type") || "";
  if (WebAssembly.instantiateStreaming && type.startsWith("application/wasm")) {
    return (await WebAssembly.instantiateStreaming(resp, go.importObject)).instance;
  }
  return (await WebAssembly.instantiate(await resp.arrayBuffer(), go.importObject)).instance;
}

window.onEzpReady = start;
(async () => {
  if (location.protocol === "file:") {
    $("status").textContent = "Open this page through a web server, not as a local file (see README).";
    return;
  }
  const go = new Go();
  try {
    go.run(await loadWasm(go));
  } catch (e) {
    $("status").textContent = `Could not load ${WASM_URL}: ${e.message}.`;
  }
})();
