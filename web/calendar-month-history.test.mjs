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
