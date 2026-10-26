import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { describe, it } from "node:test";
import vm from "node:vm";

import "./power-words.js";
import { derivePlanBrief } from "./plan-brief.js";

// FTW's rule: the UI never shows a raw minus. It shows a size, and a short
// word says the direction: import or export, charge or discharge.
const words = globalThis.ftwPowerWords;
const read = (path) => readFileSync(new URL(`./${path}`, import.meta.url), "utf8");

// One function's source, found by name and closed by matching braces, so a
// test does not hang on whatever happens to follow it in the file.
function fn(source, name) {
  const start = source.search(new RegExp(`function ${name}\\(`));
  assert.ok(start >= 0, `function ${name} not found`);
  const open = source.indexOf("{", start);
  let depth = 0;
  for (let i = open; i < source.length; i++) {
    if (source[i] === "{") depth++;
    else if (source[i] === "}" && --depth === 0) return source.slice(start, i + 1);
  }
  throw new Error(`function ${name} is not closed`);
}
function run(code, context = {}) {
  vm.createContext(context);
  vm.runInContext(code, context);
  return context;
}
const NEGATIVE = [-0.4, -5, -10.5, -950, -999.6, -1000, -1234, -12700];
const noMinus = (text) => assert.doesNotMatch(String(text), /[-−]/, String(text));
const noArrow = (text) => assert.doesNotMatch(String(text), /[↑↓]/, String(text));

describe("power in words", () => {
  it("gives any power as a size", () => {
    for (const w of NEGATIVE) {
      noMinus(words.formatW(w));
      assert.equal(words.formatW(w), words.formatW(-w));
    }
    assert.equal(words.formatW(-1234), "1.2 kW");
    assert.equal(words.formatW(null), "—");
  });

  it("says the direction in a word for both signs, and none near zero", () => {
    const cases = [
      [500, "grid", "500 W import"], [-500, "grid", "500 W export"],
      [1400, "battery", "1.4 kW charge"], [-1400, "battery", "1.4 kW discharge"],
      [3000, "v2x", "3.0 kW charge"], [-3000, "v2x", "3.0 kW discharge"],
      [8, "grid", "8 W"], [-8, "battery", "8 W"], [-1400, "solar", "1.4 kW"],
    ];
    for (const [w, kind, want] of cases) {
      assert.equal(words.formatPower(w, kind), want, `${w} ${kind}`);
      noMinus(words.formatPower(w, kind));
      noArrow(words.formatPower(w, kind));
    }
  });

  it("names a target's direction only when it differs from the reading's", () => {
    assert.equal(words.formatTarget(-1200, -1100, "battery"), "→ 1.2 kW");
    assert.equal(words.formatTarget(800, -1100, "battery"), "→ 800 W charge");
    assert.equal(words.formatTarget(-2000, 5, "grid"), "→ 2.0 kW export");
  });

  it("says how far a device is from its target", () => {
    assert.equal(words.trackingText(-700, -1000), "300 W short");
    assert.equal(words.trackingText(1300, 1000), "300 W over");
    assert.equal(words.trackingText(-1005, -1000), "on target");
  });

  it("names both sides of every chart in words, with one separator", () => {
    for (const [chart, sides] of Object.entries(words.SIDES)) {
      for (const text of [sides.above, sides.below]) {
        noArrow(text);
        assert.doesNotMatch(text, /[/,;]/, `${chart}: ${text}`);
      }
    }
    assert.equal(words.SIDES.live.below, "export · discharge · solar");
    assert.equal(words.SIDES.live.above, "import · charge · home · car");
    const drawn = [];
    const ctx = {
      save() {}, restore() {}, fillRect() { drawn.push("back"); }, measureText: (t) => ({ width: t.length * 5 }),
      fillText(text, x, y) { drawn.push([text, y]); },
    };
    words.drawSides(ctx, 10, 100, words.SIDES.battery, "ink", "panel");
    assert.deepEqual(drawn, ["back", ["charge", 93], "back", ["discharge", 107]]);
  });
});

describe("the dashboard uses the words where a power goes both ways", () => {
  const app = read("app.js");
  const helpers = ["powerWords", "formatW", "formatPower", "liveStatParts", "updateLiveStat", "batteryTargetLine", "tooltipPowerText", "batteryRowParts"]
    .map((name) => fn(app, name)).join("\n");

  function dashboard() {
    const els = {};
    // Just enough of an element: a child's text joins its parent's.
    const element = () => ({
      textContent: "", className: "",
      appendChild(child) { this.textContent += child.textContent; },
    });
    const document = { getElementById: (id) => (els[id] ||= element()), createElement: element };
    return { els, ...run(helpers, { ftwPowerWords: words, document }) };
  }

  it("writes the live strip with a word for the grid and the battery", () => {
    const d = dashboard();
    d.updateLiveStat("bat", -1400, "is-discharging");
    d.updateLiveStat("grid", -500, "is-export");
    d.updateLiveStat("load", 900, "is-neutral");
    assert.equal(d.els["live-stat-bat"].textContent, "1.4 kW discharge");
    assert.equal(d.els["live-stat-grid"].textContent, "500 W export");
    assert.equal(d.els["live-stat-load"].textContent, "900 W");
    d.updateLiveStat("bat", 1400, "is-charging");
    assert.equal(d.els["live-stat-bat"].textContent, "1.4 kW charge");
  });

  it("writes the battery target and the tooltip rows with their words", () => {
    const d = dashboard();
    assert.equal(d.batteryTargetLine(-1500), "target 1.5 kW discharge");
    assert.deepEqual({ ...d.tooltipPowerText({ name: "Grid", data: [-500] }, 0) }, { actual: "500 W export", target: "" });
    assert.deepEqual({ ...d.tooltipPowerText({ name: "Sungrow", data: [-1100], target: [-1200] }, 0) },
      { actual: "1.1 kW discharge", target: "→ 1.2 kW" });
    assert.deepEqual({ ...d.tooltipPowerText({ name: "Sungrow", data: [-1100], target: [800] }, 0) },
      { actual: "1.1 kW discharge", target: "→ 800 W charge" });
    assert.deepEqual({ ...d.tooltipPowerText({ name: "Easee EV", data: [3000] }, 0) }, { actual: "3.0 kW", target: "" });
    assert.deepEqual({ ...d.batteryRowParts(-700, -1000) }, { actual: "700 W discharge", target: "→ 1.0 kW", tracking: "300 W short" });
    assert.deepEqual({ ...d.batteryRowParts(1300, -1000) }, { actual: "1.3 kW charge", target: "→ 1.0 kW discharge", tracking: "300 W over" });
  });

  it("never prints a signed grid or battery power as a bare size", () => {
    // Each of these can be negative and has no word beside it on screen.
    // (The V2X confirm prints formatW(powerW) after the word "Discharge".)
    const signed = ["batWVal", "meterW", "d.target_w", "disp.target_w", "v2xWVal", "sw", "dev",
      "data.grid_target_w", "Number(gridTargetSlider.value)", "targetW", "t", "d.v2x_dc_w"];
    for (const expr of signed) {
      assert.ok(!app.includes(`formatW(${expr})`), `formatW(${expr}) drops the direction`);
    }
    // The grid and battery cards print the size and set the word on the next lines.
    for (const [value, word] of [["data.grid_w", "gridDir"], ["data.bat_w", "batDir"]]) {
      const at = app.indexOf(`formatW(${value})`);
      assert.ok(at > 0 && app.slice(at, at + 400).includes(`${word}.textContent`), value);
      assert.equal(app.indexOf(`formatW(${value})`, at + 1), -1, `${value} is printed bare elsewhere`);
    }
  });
});

describe("charts show sizes, never a raw minus", () => {
  it("labels the plan and diagnose power axes as sizes", () => {
    for (const path of ["plan.js", "diagnose.js"]) {
      const { powerAxisLabel } = run(fn(read(path), "powerAxisLabel"));
      assert.equal(powerAxisLabel(-12700), "12.7kW", path);
      for (const w of NEGATIVE) noMinus(powerAxisLabel(w));
    }
  });

  it("starts no chart text with a sign", () => {
    for (const path of ["app.js", "plan.js", "diagnose.js"]) {
      assert.doesNotMatch(read(path), /fillText\(\s*['"`][-−+]['"`]?\s*\+/, path);
    }
  });

  it("draws the words beside zero on every chart with a lower side", () => {
    const app = read("app.js");
    assert.match(app, /drawSides\(ctx, pad\.left \+ 6, zeroLabelY, powerWords\(\)\.SIDES\.live,/);
    assert.match(app, /drawSides\(ctx, pad\.left \+ 4, batMid, powerWords\(\)\.SIDES\.battery,/);
    assert.match(read("plan.js"), /drawSides\(ctx, pad\.l \+ 6, powerYCenter, globalThis\.ftwPowerWords\.SIDES\.plan,/);
    assert.match(read("diagnose.js"), /drawSides\(ctx, pad\.l \+ 6, powerMidY, globalThis\.ftwPowerWords\.SIDES\.diagnose,/);
    // Loaded before the dashboard script that reads it.
    const html = read("index.html");
    assert.ok(html.indexOf('src="/power-words.js"') < html.indexOf('src="/app.js'));
  });

  it("prints the flow bubble as a size, with the word under it", () => {
    const { fmtKw } = run(fn(read("components/ftw-energy-flow.js"), "fmtKw"));
    assert.equal(fmtKw(-1.2), "1.20 kW");
    assert.equal(fmtKw(-0.012), "12 W");
    assert.equal(fmtKw(-0.0004), "0 W");
  });

  it("says the battery's direction in the plan tooltip and brief", () => {
    assert.match(read("plan.js"), /\$\{\(Math\.abs\(a\.battery_w\) \/ 1000\)\.toFixed\(1\)\} kW \(\$\{dir\}\)/);
    const now = Date.now();
    const brief = derivePlanBrief({
      enabled: true,
      plan: { actions: [{ slot_start_ms: now, slot_len_min: 15, battery_w: -1800, loadpoint_w: 0, pv_limit_w: 0 }] },
      status: { mode: "planner_passive_arbitrage", dispatch: [{ driver: "battery", clamped: true, target_w: -1800 }] },
      now,
    });
    assert.match(brief.constraint, /Safety adjusted battery to 1\.8 kW discharge/);
    noMinus(brief.constraint);
  });
});
