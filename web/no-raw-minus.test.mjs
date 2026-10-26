import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { describe, it } from "node:test";
import vm from "node:vm";

import { derivePlanBrief } from "./plan-brief.js";

// FTW's rule: the UI never shows a raw minus. It shows a size, and says the
// direction in a word or with an arrow.
const read = (path) => readFileSync(new URL(`./${path}`, import.meta.url), "utf8");
const between = (source, start, end) => {
  const from = source.indexOf(start);
  const to = source.indexOf(end, from);
  assert.ok(from >= 0 && to > from, `${start} … ${end}`);
  return source.slice(from, to);
};
const run = (code) => {
  const context = {};
  vm.createContext(context);
  vm.runInContext(code, context);
  return context;
};
const NEGATIVE = [-0.4, -5, -10.5, -950, -999.6, -1000, -1234, -12700];
const noMinus = (text) => assert.doesNotMatch(String(text), /[-−]/, String(text));

describe("charts show sizes, never a raw minus", () => {
  const app = read("app.js");
  const live = run(between(app, "  function formatW(", "  // Fuse bar color."));

  it("formats any power as a size", () => {
    for (const w of NEGATIVE) {
      noMinus(live.formatW(w));
      noMinus(live.formatFlowW(w));
      assert.equal(live.formatW(w), live.formatW(-w));
    }
    assert.equal(live.formatW(null), "—");
  });

  it("marks the side of zero where a power goes both ways", () => {
    assert.equal(live.formatFlowW(1200), "1.2 kW ↑");
    assert.equal(live.formatFlowW(-1200), "1.2 kW ↓");
    assert.equal(live.formatFlowW(-500), "500 W ↓");
    assert.equal(live.formatFlowW(5), "5 W");
    assert.equal(live.formatFlowW(-5), "5 W");
  });

  it("labels the plan and diagnose power axes as sizes", () => {
    for (const path of ["plan.js", "diagnose.js"]) {
      const source = read(path);
      const { powerAxisLabel } = run(between(source, "  function powerAxisLabel(", "\n  }\n") + "\n  }");
      assert.equal(powerAxisLabel(-12700), "12.7kW", path);
      assert.equal(powerAxisLabel(12700), "12.7kW", path);
      for (const w of NEGATIVE) noMinus(powerAxisLabel(w));
      assert.doesNotMatch(source, /fillText\(\s*['"][-−+]['"]\s*\+/, path);
    }
  });

  it("says what sits below zero on every chart with a lower side", () => {
    // Live power chart.
    assert.match(app, /var BELOW_ZERO_LIVE = "↓ export · discharge · solar";/);
    assert.match(app, /ctx\.fillText\(BELOW_ZERO_LIVE,/);
    // 24 h battery chart: both edges are sizes, the words name the sides.
    assert.doesNotMatch(app, /"-" \+ \(batMax/);
    assert.match(app, /var BATTERY_SIDES = "↑ charge {3}↓ discharge";/);
    assert.match(app, /ctx\.fillText\(BATTERY_SIDES,/);
    // Plan power band.
    const plan = read("plan.js");
    assert.match(plan, /const POWER_SIDES = '↑ import \/ charge {3}↓ export \/ discharge \/ solar';/);
    assert.match(plan, /ctx\.fillText\(POWER_SIDES,/);
    // Diagnose chart.
    const diagnose = read("diagnose.js");
    assert.match(diagnose, /var POWER_BELOW = '↓ export · discharge · solar';/);
    assert.match(diagnose, /ctx\.fillText\(POWER_BELOW,/);
  });

  it("keeps the words, not the sign, in the plan tooltip and brief", () => {
    assert.match(read("plan.js"), /\$\{\(Math\.abs\(a\.battery_w\) \/ 1000\)\.toFixed\(1\)\} kW \(\$\{dir\}\)/);
    const now = Date.now();
    const brief = derivePlanBrief({
      enabled: true,
      plan: { actions: [{ slot_start_ms: now, slot_len_min: 15, battery_w: -1800, loadpoint_w: 0, pv_limit_w: 0 }] },
      status: { mode: "planner_passive_arbitrage", dispatch: [{ driver: "battery", clamped: true, target_w: -1800 }] },
      now,
    });
    assert.match(brief.constraint, /Safety adjusted battery to 1\.8 kW ↓/);
    noMinus(brief.constraint);
  });
});
