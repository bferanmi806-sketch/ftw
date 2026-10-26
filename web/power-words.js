// Power in words. The UI never shows a raw minus: it shows a size, and a
// short word says which way the power goes. Above zero sits what adds to
// the grid meter's reading (import, charge, the home, the car); below sits
// what takes away from it (export, discharge, solar).
//
// One file for every page: index.html loads it as a plain script before
// app.js, and modules import it for its side effect. It has no import or
// export of its own, so both ways read the same globalThis.ftwPowerWords.
(function (root) {
  "use strict";

  // The only direction words, as the phone app uses them.
  var WORDS = {
    grid: ["import", "export"],
    battery: ["charge", "discharge"],
    v2x: ["charge", "discharge"],
  };
  // Within this of zero a power has no direction worth a word.
  var QUIET_W = 10;

  // The size of a power: "500 W", "1.2 kW". Never signed.
  function formatW(w) {
    if (w == null || !isFinite(w)) return "—";
    var abs = Math.abs(w);
    if (abs >= 1000) return (abs / 1000).toFixed(1) + " kW";
    return Math.round(abs) + " W";
  }

  // "import" or "export" for the grid, "charge" or "discharge" for a
  // battery or a two-way charger; "" near zero or for anything else.
  function directionWord(w, kind) {
    var words = WORDS[kind];
    if (!words || w == null || !isFinite(w) || Math.abs(w) <= QUIET_W) return "";
    return w > 0 ? words[0] : words[1];
  }

  // A two-way power as a size and its word: "1.4 kW discharge".
  function formatPower(w, kind) {
    var word = directionWord(w, kind);
    return formatW(w) + (word ? " " + word : "");
  }

  // A target beside a reading: its size, and its word only when it goes
  // the other way from the reading, so "→ 1.2 kW" rather than repeating
  // "discharge".
  function formatTarget(targetW, actualW, kind) {
    var word = directionWord(targetW, kind);
    return "→ " + (word && word !== directionWord(actualW, kind) ? formatPower(targetW, kind) : formatW(targetW));
  }

  // How far a device is from its target, by size: "300 W short" when it
  // moves less power than asked, "300 W over" when more.
  function trackingText(actualW, targetW) {
    var gap = Math.abs(Number(actualW) || 0) - Math.abs(Number(targetW) || 0);
    if (Math.abs(gap) <= QUIET_W) return "on target";
    return formatW(gap) + (gap < 0 ? " short" : " over");
  }

  // What each chart draws above and below zero, where its axis labels are
  // sizes. One separator everywhere.
  var SIDES = {
    live: { above: "import · charge · home · car", below: "export · discharge · solar" },
    battery: { above: "charge", below: "discharge" },
    plan: { above: "charge · home · car", below: "discharge · solar" },
    diagnose: { above: "import · charge · car", below: "export · discharge · solar" },
  };

  // Write a chart's two sides beside its zero line, at x. Each word sits on
  // a backing in the panel colour, so a series line under it cannot make it
  // unreadable. The caller sets the font.
  function drawSides(ctx, x, zeroY, sides, ink, panel) {
    var rows = [[sides.above, zeroY - 7], [sides.below, zeroY + 7]];
    ctx.save();
    ctx.textAlign = "left";
    ctx.textBaseline = "middle";
    for (var i = 0; i < rows.length; i++) {
      var text = rows[i][0];
      var y = rows[i][1];
      var w = ctx.measureText(text).width;
      ctx.globalAlpha = 0.85;
      ctx.fillStyle = panel;
      ctx.fillRect(x - 3, y - 6, w + 6, 12);
      ctx.globalAlpha = 1;
      ctx.fillStyle = ink;
      ctx.fillText(text, x, y);
    }
    ctx.restore();
  }

  root.ftwPowerWords = {
    formatW: formatW,
    directionWord: directionWord,
    formatPower: formatPower,
    formatTarget: formatTarget,
    trackingText: trackingText,
    SIDES: SIDES,
    drawSides: drawSides,
  };
})(globalThis);
