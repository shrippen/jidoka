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

  // Times Jidoka formats itself are in its zone (jidoka.toml). Where the
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

  // Every htmx request carries the page's CSRF token (Z4).
  d.addEventListener("htmx:configRequest", function (e) {
    var meta = d.querySelector('meta[name="csrf"]');
    if (meta) {
      e.detail.headers["X-CSRF-Token"] = meta.getAttribute("content");
    }
  });
})();
