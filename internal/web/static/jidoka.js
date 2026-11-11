/* Jidoka's page script: theme, zone marks, CSRF for htmx. Loaded
   synchronously in <head> (the theme must be set before the first paint);
   the rest waits for the document. */
(function () {
  "use strict";
  var d = document;

  // Kante's tokens are dark; the light theme comes with data-theme="light".
  // It follows the system setting, live.
  var light = window.matchMedia ? window.matchMedia("(prefers-color-scheme: light)") : null;

  function theme() {
    d.documentElement.setAttribute("data-theme", light && light.matches ? "light" : "dark");
  }
  theme();
  if (light && light.addEventListener) {
    light.addEventListener("change", theme);
  }

  // Times Jidoka formats itself are in its zone (settings). Where the
  // browser's zone differs, their .tz-mark names it: "12:26 UTC".
  // Adapted from andon internal/web/static/andon.js showZones (468474a).
  function showZones(root) {
    var here = -new Date().getTimezoneOffset();
    [].forEach.call(root.querySelectorAll(".tz-mark"), function (mark) {
      mark.hidden = parseInt(mark.getAttribute("data-offset"), 10) === here;
    });
  }

  d.addEventListener("DOMContentLoaded", function () { showZones(d); });
  d.addEventListener("htmx:load", function (e) { showZones(e.target); });

  // Board filter (O1), client side: tiles carry data-kind, data-tags and
  // data-attn. The 15 s poll swaps the whole board, so the choice is kept
  // here and applied again after every swap.
  var pick = { kind: "all", tags: [] };

  function showBoard() {
    var board = d.getElementById("board");
    if (!board) {
      return;
    }

    [].forEach.call(board.querySelectorAll("[data-filter]"), function (b) {
      b.setAttribute("aria-pressed", String(b.getAttribute("data-filter") === pick.kind));
    });
    [].forEach.call(board.querySelectorAll("[data-tag]"), function (b) {
      b.setAttribute("aria-pressed", String(pick.tags.indexOf(b.getAttribute("data-tag")) >= 0));
    });

    var shown = 0;
    [].forEach.call(board.querySelectorAll(".board-tile"), function (t) {
      var own = (t.getAttribute("data-tags") || "").split(" ");
      var ok = true;
      if (pick.kind === "attn") {
        ok = t.hasAttribute("data-attn");
      } else if (pick.kind !== "all") {
        ok = t.getAttribute("data-kind") === pick.kind;
      }
      if (ok && pick.tags.length) {
        ok = pick.tags.some(function (tag) { return own.indexOf(tag) >= 0; });
      }
      t.hidden = !ok;
      shown += ok ? 1 : 0;
    });

    var none = board.querySelector(".board-none");
    if (none) {
      none.hidden = shown !== 0;
    }
  }

  d.addEventListener("click", function (e) {
    var el = e.target.closest ? e.target : e.target.parentElement;
    if (!el) {
      return;
    }

    var kind = el.closest("[data-filter]");
    if (kind) {
      pick.kind = kind.getAttribute("data-filter");
      showBoard();
      return;
    }

    var tag = el.closest("[data-tag]");
    if (tag) {
      var name = tag.getAttribute("data-tag"), at = pick.tags.indexOf(name);
      if (at >= 0) {
        pick.tags.splice(at, 1);
      } else {
        pick.tags.push(name);
      }
      showBoard();
      return;
    }

    // A tile opens its job; links, buttons and forms inside it keep their own job.
    var tile = el.closest(".board-tile[data-href]");
    if (tile && !el.closest("a, button, form")) {
      location.href = tile.getAttribute("data-href");
    }
  });

  d.addEventListener("DOMContentLoaded", showBoard);
  d.addEventListener("htmx:afterSwap", showBoard);

  // Settings page: a row of sub channels is added from a <template> or
  // removed. Without script the form still holds one blank row to fill.
  d.addEventListener("click", function (e) {
    var el = e.target.closest ? e.target : e.target.parentElement;
    var add = el && el.closest("[data-add-row]");
    var drop = el && el.closest("[data-remove-row]");

    if (add) {
      var tpl = d.getElementById(add.getAttribute("data-add-row"));
      add.parentNode.parentNode.insertBefore(tpl.content.cloneNode(true), tpl);
    }
    if (drop) {
      drop.closest("[data-row]").remove();
    }
  });

  // Every htmx request carries the page's CSRF token (Z4).
  d.addEventListener("htmx:configRequest", function (e) {
    var meta = d.querySelector('meta[name="csrf"]');
    if (meta) {
      e.detail.headers["X-CSRF-Token"] = meta.getAttribute("content");
    }
  });
})();
