import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const html = readFileSync(new URL("./index.html", import.meta.url), "utf8");
const app = readFileSync(new URL("./app.js", import.meta.url), "utf8");
const card = readFileSync(new URL("./components/ftw-history-card.js", import.meta.url), "utf8");

test("history offers week, this month, last month and an accessible month picker", () => {
  assert.match(html, /data-range="week"[^>]*>Week/);
  assert.match(html, /data-range="this-month"[^>]*>This month/);
  assert.match(html, /data-range="last-month"[^>]*>Last month/);
  assert.match(html, /type="month" aria-label="Choose history month"/);
});

test("shared bars and pie request the same selected calendar month", () => {
  assert.match(app, /month=" \+ encodeURIComponent\(historyState\.range\.slice\(6\)\)/);
  assert.match(app, /tile\.setAttribute\("range", historyState\.range\)/);
  assert.match(card, /month=" \+ encodeURIComponent\(month\)/);
  assert.match(card, /function currentMonth\(\)/);
});

test("a redundant range notification does not reset a loaded history card", () => {
  const callback = card.match(/attributeChangedCallback\(name\)\s*\{([\s\S]*?)\n  \}/)?.[1];
  assert.ok(callback, "history-card attribute callback exists");

  // FtwElement.update() replaces the card shadow DOM. Same-range attribute
  // writes must therefore return before update(), or they recreate a loading
  // chart without a corresponding refresh.
  assert.match(callback, /if \(!next \|\| next === this\._range\) return;/);
  assert.match(callback, /this\.update\(\);[\s\S]*if \(rangeChanged\) this\._refresh\(\);/);
  assert.match(card, /if \(seq !== this\._reqSeq\) return;/);

  const onAttributeChanged = new Function("name", callback);
  const cards = Array.from({ length: 4 }, () => {
    const el = {
      range: "month:2026-10",
      _range: "month:2026-10",
      updates: 0,
      refreshes: 0,
      getAttribute(name) { return name === "range" ? this.range : null; },
      update() { this.updates++; },
      _refresh() { this.refreshes++; },
    };
    return el;
  });

  for (const range of ["month:2026-09", "month:2025-03", "month:2026-10"]) {
    for (const el of cards) {
      el.range = range;
      onAttributeChanged.call(el, "range");
      assert.equal(el.updates, el.refreshes, "a real selection updates and refreshes");
      assert.equal(el._range, range);
      // The app may redundantly write its authoritative range. This must not
      // recreate the shadow DOM after the response has rendered the chart.
      onAttributeChanged.call(el, "range");
      assert.equal(el.updates, el.refreshes);
    }
  }
});

test("shared month controls target all four history tiles", () => {
  assert.match(html, /id="history-tiles"/);
  assert.equal((html.match(/<ftw-history-card metric=/g) || []).length, 4);
  assert.match(app, /querySelectorAll\("\.history-tiles ftw-history-card"\)/);
  assert.match(app, /historyState\.range/);
});
