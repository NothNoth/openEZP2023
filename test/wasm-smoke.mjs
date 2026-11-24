// Loads a built dist/ (or web/) folder in Node and runs the JS API against
// the simulated programmer: the same code path as the browser, minus WebUSB.
// Usage: node test/wasm-smoke.mjs dist
import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

const dir = path.resolve(process.argv[2] || "dist");
const require = createRequire(import.meta.url);
globalThis.require = require;
globalThis.fs = fs;
require(path.join(dir, "wasm_exec.js"));

const go = new Go();
const ready = new Promise((resolve) => (globalThis.onEzpReady = resolve));
const { instance } = await WebAssembly.instantiate(fs.readFileSync(path.join(dir, "ezp.wasm")), go.importObject);
go.run(instance);
await ready;
const ezp = globalThis.ezp;

function check(cond, msg) {
  if (!cond) { console.error("FAIL:", msg); process.exit(1); }
  console.log("ok:", msg);
}

check(ezp.chips.length > 900, `chip database loaded (${ezp.chips.length} chips)`);
const chip = ezp.chips.find((c) => c.model === "W25Q80");
check(chip && chip.jedec === "EF4014", "W25Q80 present with its JEDEC ID");
check(ezp.chips.find((c) => c.model === "MX25U3235F")?.needs18V === true, "1.8 V detection exported");
check(ezp.chips.find((c) => c.model === "MX66U51235F")?.needs18V === true, "MX66U family flagged 1.8 V");

await ezp.connectDemo(chip.index);
const d = await ezp.detect();
check(d.jedec === "EF4014" && d.matches.includes(chip.index), "detect finds the chip");
check(d.signatureOK && d.signature === "9A7336BD", "detect checks the firmware signature");
await ezp.erase(chip.index);
check((await ezp.blankCheck(chip.index)).ok, "blank after erase");
const img = new Uint8Array(100000).map((_, i) => (i * 31) & 0xff);
await ezp.write(chip.index, img);
check((await ezp.verify(chip.index, img)).ok, "verify after write");
const back = await ezp.read(chip.index);
check(back.length === 1 << 20 && back.slice(0, img.length).every((b, i) => b === img[i]), "read back matches");
img[4242] ^= 1;
check((await ezp.verify(chip.index, img)).offset === 4242, "verify reports the mismatch offset");
process.exit(0);
