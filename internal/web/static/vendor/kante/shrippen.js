/* Kante v1 — language switch, nav brand reveal, copy button, motion.
 * Load synchronously in <head> so the stored language applies before first paint.
 * Motion (entrances, counters, typing, sliding tabs, scroll meter) only runs when
 * the reader has not asked for reduced motion; html.motion marks that. */
(function () {
  var d = document, h = d.documentElement, KEY = 'shrippen-lang';
  h.classList.add('js');
  var still = window.matchMedia && matchMedia('(prefers-reduced-motion: reduce)').matches;
  if (!still && 'IntersectionObserver' in window) h.classList.add('motion');

  // An app with its own language handling (Andon) puts data-own-lang on <html>: Kante then
  // leaves <html lang> and the .lang switch alone.
  var ownLang = h.hasAttribute('data-own-lang');
  var lang = 'en'; // English is the default, whatever the browser language is
  try { var s = localStorage.getItem(KEY); if (s === 'de' || s === 'en') lang = s; } catch (e) {}
  if (!ownLang) h.lang = lang;

  function mark() {
    if (ownLang) return;
    [].forEach.call(d.querySelectorAll('.lang button'), function (b) {
      b.setAttribute('aria-pressed', String(b.getAttribute('data-lang') === h.lang));
    });
  }

  // Suggestion under a field (1.72): a click on a value puts it into the field named by the
  // line's data-suggest-for (an id), as if typed: input and change bubble, so the app's own
  // handlers run. The value is data-value, else the button's text; a select takes it only if
  // one of its options has it. A missing, disabled (also by its fieldset) or read-only field
  // stays as it is. A line without data-suggest-for is the app's own: Kante leaves it alone.
  function pickValue(btn) {
    return btn.hasAttribute('data-value') ? btn.getAttribute('data-value') : btn.textContent.trim();
  }

  function suggest(btn) {
    var line = btn.closest('.field-suggest[data-suggest-for]');
    var f = line && d.getElementById(line.getAttribute('data-suggest-for'));
    if (!f || !/^(INPUT|TEXTAREA|SELECT)$/.test(f.tagName)) return;
    if (f.readOnly || f.matches(':disabled')) return;
    var v = pickValue(btn);
    if (f.tagName === 'SELECT' && ![].some.call(f.options, function (o) { return o.value === v; })) return;
    f.value = v;
    f.dispatchEvent(new Event('input', { bubbles: true }));
    f.dispatchEvent(new Event('change', { bubbles: true }));
  }

  // The picks follow the field, whoever changed it: the one equal to its value is
  // aria-pressed="true" (shown as taken), the others "false". E.g. typing over a taken
  // "Hosting" turns it back into a link.
  function markPicks(e) {
    var f = e.target;
    if (!f.id || !/^(INPUT|TEXTAREA|SELECT)$/.test(f.tagName)) return;
    var sel = '.field-suggest[data-suggest-for="' + (window.CSS && CSS.escape ? CSS.escape(f.id) : f.id) + '"] .field-suggest-pick';
    [].forEach.call(d.querySelectorAll(sel), function (b) {
      b.setAttribute('aria-pressed', String(pickValue(b) === f.value));
    });
  }
  d.addEventListener('input', markPicks);
  d.addEventListener('change', markPicks);

  d.addEventListener('DOMContentLoaded', function () {
    mark();

    d.addEventListener('click', function (e) {
      var lb = !ownLang && e.target.closest && e.target.closest('.lang button');
      if (lb) {
        h.lang = lb.getAttribute('data-lang');
        try { localStorage.setItem(KEY, h.lang); } catch (x) {}
        mark();
        return;
      }
      var sp = e.target.closest && e.target.closest('.field-suggest-pick');
      if (sp) {
        suggest(sp);
        return;
      }
      var cb = e.target.closest && e.target.closest('.copy-btn');
      if (cb) {
        var box = cb.closest('.cmd-row').querySelector('.cmd-box');
        navigator.clipboard.writeText(box.textContent.trim()).then(function () {
          cb.classList.add('copied');
          setTimeout(function () { cb.classList.remove('copied'); }, 1500);
        });
      }
    });

    // Project name in the nav appears once the hero is out of view.
    var nav = d.querySelector('.nav'), hero = d.querySelector('.hero');
    if (nav && hero && 'IntersectionObserver' in window) {
      new IntersectionObserver(function (en) {
        nav.classList.toggle('brand-on', !en[0].isIntersecting);
      }, { rootMargin: '-56px 0px 0px 0px' }).observe(hero);
    } else if (nav) {
      nav.classList.add('brand-on');
    }

    // Scroll meter under the nav (A20).
    if (nav) {
      var meter = d.createElement('span');
      meter.className = 'nav-meter';
      meter.setAttribute('aria-hidden', 'true');
      nav.appendChild(meter);
      var onScroll = function () {
        var max = d.documentElement.scrollHeight - innerHeight;
        nav.style.setProperty('--scroll', max > 0 ? Math.min(1, scrollY / max) : 0);
      };
      addEventListener('scroll', onScroll, { passive: true });
      onScroll();
    }

    // Tabs: the yellow tab slides to the selected one (A11).
    [].forEach.call(d.querySelectorAll('.tabs'), function (tabs) {
      var ind = d.createElement('span');
      ind.className = 'tabs-ind';
      ind.setAttribute('aria-hidden', 'true');
      tabs.insertBefore(ind, tabs.firstChild);
      tabs.classList.add('has-ind');
      var place = function () {
        var cur = tabs.querySelector('[aria-selected="true"],[aria-current="page"]');
        ind.style.width = cur ? cur.offsetWidth + 'px' : '0';
        ind.style.left = cur ? cur.offsetLeft + 'px' : '0';
      };
      tabs.addEventListener('click', function (e) {
        var t = e.target.closest('[role="tab"]');
        if (!t) return;
        [].forEach.call(tabs.querySelectorAll('[role="tab"]'), function (x) { x.setAttribute('aria-selected', String(x === t)); });
        place();
      });
      place();
      addEventListener('resize', place);
      if (d.fonts) d.fonts.ready.then(place);
    });

    // ── Nav drawer and filter side (Kante 1.19) ──────────────────────────
    // .nav-burger[aria-controls] opens its <dialog class="nav-drawer"> as a modal (focus moves in);
    // Esc, a click on the scrim or on [data-drawer-close] closes it, focus returns to the button.
    // .filter-toggle[aria-controls] flips aria-expanded; CSS shows the .filter-side below 640px.
    // Delegated, so a nav swapped in later (hx-boost) works without a mount call.
    function burgerOf(drawer) { return drawer.id ? d.querySelector('.nav-burger[aria-controls="' + drawer.id + '"]') : null; }
    d.addEventListener('click', function (e) {
      var t = e.target;
      if (!t.closest) return;
      var burger = t.closest('.nav-burger[aria-controls]');
      if (burger) {
        var dr = d.getElementById(burger.getAttribute('aria-controls'));
        if (!dr || dr.open || !dr.showModal) return;
        dr.showModal();
        burger.setAttribute('aria-expanded', 'true');
        return;
      }
      var open = t.closest('dialog.nav-drawer[open]');
      if (open) {
        var r = open.getBoundingClientRect(), out = e.clientX < r.left || e.clientX > r.right || e.clientY < r.top || e.clientY > r.bottom;
        if ((t === open && out) || t.closest('[data-drawer-close]')) open.close();
        return;
      }
      var ft = t.closest('.filter-toggle[aria-controls]');
      if (ft) {
        var on = ft.getAttribute('aria-expanded') !== 'true';
        ft.setAttribute('aria-expanded', String(on));
        var side = d.getElementById(ft.getAttribute('aria-controls'));
        var first = on && side && side.querySelector('input,a,button,summary');
        if (first) first.focus();
      }
    });
    // close does not bubble: listen while capturing.
    d.addEventListener('close', function (e) {
      var dr = e.target;
      if (!dr.matches || !dr.matches('dialog.nav-drawer')) return;
      var b = burgerOf(dr);
      if (!b) return;
      b.setAttribute('aria-expanded', 'false');
      b.focus();
    }, true);
    // Esc in an open filter panel closes it and returns to its button.
    d.addEventListener('keydown', function (e) {
      if (e.key !== 'Escape' || !e.target.closest) return;
      var side = e.target.closest('.filter-side[id]');
      var ft = side && d.querySelector('.filter-toggle[aria-controls="' + side.id + '"][aria-expanded="true"]');
      if (!ft) return;
      ft.setAttribute('aria-expanded', 'false');
      ft.focus();
    });
    // A drawer left open while the window grows past the phone width closes.
    if (window.matchMedia) {
      var wide = matchMedia('(min-width: 641px)'), shut = function () {
        if (!wide.matches) return;
        [].forEach.call(d.querySelectorAll('dialog.nav-drawer[open]'), function (dr) { dr.close(); });
      };
      if (wide.addEventListener) wide.addEventListener('change', shut);
    }

    // ── Chart read-out and curve editor (Kante 1.6, 1.15) ────────────────
    // .chart-wrap[data-readout] > svg.chart: on hover a rule and marks sit on the nearest point
    //   of each .line (polyline points or path d), .readout shows data-labels (svg) and
    //   data-values (each .line) at that index, split on "|" if present ("1,5|2,25"), else ",". An element with data-tip (a bar, a cell, a
    //   strip) shows its own text instead. Delegated, so charts added later work too.
    // svg.curve[data-editable]: drag .pt squares, arrows move the focused one; fires "change"
    //   with detail = [{x, y}]. data-x/y-min/max and data-step give the range.
    var NS = 'http://www.w3.org/2000/svg', reading = null;
    function linePoints(l) {
      var raw = l.getAttribute('points') || l.getAttribute('d') || '', n = raw.match(/-?[\d.]+(?:e-?\d+)?/gi) || [], out = [];
      for (var i = 0; i + 1 < n.length; i += 2) out.push([+n[i], +n[i + 1]]);
      return out;
    }
    // data-labels / data-values: split on "|" when present (values with decimal commas), else ",".
    function list(v) { v = v || ''; return v.split(v.indexOf('|') >= 0 ? '|' : ','); }
    function esc(t) { var x = d.createElement('span'); x.textContent = t; return x.innerHTML; }
    function readOff(wrap) {
      if (!wrap) return;
      [].forEach.call(wrap.querySelectorAll('.probe,.mark'), function (m) { m.style.display = 'none'; });
      wrap.classList.remove('is-reading');
    }
    function readTip(wrap, tip, html, left, width) {
      tip.innerHTML = html;
      wrap.classList.add('is-reading');
      var w = tip.offsetWidth;
      tip.style.left = (left > width / 2 ? left - w - 8 : left + 8) + 'px';
    }
    function readLines(wrap, svg, tip, e) {
      var lines = svg.querySelectorAll('.line');
      if (!lines.length) return false;
      var pts = [].map.call(lines, linePoints);
      if (!pts[0].length) return false;
      var probe = svg.querySelector('.probe');
      if (!probe) {
        probe = d.createElementNS(NS, 'line'); probe.setAttribute('class', 'probe'); svg.appendChild(probe);
        [].forEach.call(lines, function (l) {
          var m = d.createElementNS(NS, 'rect');
          m.setAttribute('class', 'mark ' + ((l.getAttribute('class') || '').match(/s\d/) || [''])[0]); m.setAttribute('width', 7); m.setAttribute('height', 7);
          svg.appendChild(m);
        });
      }
      var marks = svg.querySelectorAll('.mark');
      var r = svg.getBoundingClientRect(), vb = svg.viewBox.baseVal, x = (e.clientX - r.left) / r.width * vb.width + vb.x;
      var best = 0;
      pts[0].forEach(function (p, i) { if (Math.abs(p[0] - x) < Math.abs(pts[0][best][0] - x)) best = i; });
      var px = pts[0][best][0], labels = list(svg.dataset.labels);
      probe.setAttribute('x1', px); probe.setAttribute('x2', px); probe.setAttribute('y1', vb.y); probe.setAttribute('y2', vb.y + vb.height);
      probe.style.display = '';
      var html = labels[best] ? '<b>' + labels[best] + '</b>' : '';
      // 7 px marks also in a stretched chart (preserveAspectRatio="none").
      var mw = 7 * vb.width / r.width, mh = 7 * vb.height / r.height;
      [].forEach.call(lines, function (l, i) {
        var p = pts[i][best]; if (!p) return;
        marks[i].setAttribute('width', mw); marks[i].setAttribute('height', mh);
        marks[i].setAttribute('x', p[0] - mw / 2); marks[i].setAttribute('y', p[1] - mh / 2); marks[i].style.display = '';
        html += list(l.dataset.values)[best] + (svg.dataset.unit || '') + ' ';
      });
      readTip(wrap, tip, html, (px - vb.x) / vb.width * r.width, r.width);
      return true;
    }
    d.addEventListener('pointermove', function (e) {
      var wrap = e.target.closest && e.target.closest('.chart-wrap[data-readout]');
      if (wrap !== reading) { readOff(reading); reading = wrap; }
      if (!wrap) return;
      var tip = wrap.querySelector('.readout'), svg = wrap.querySelector('svg');
      if (!tip) return;
      var one = e.target.closest('[data-tip]');
      if (one && wrap.contains(one)) {
        readOff(wrap);
        // "Label · value": the label above, as in the line read-out.
        var r = wrap.getBoundingClientRect(), text = one.getAttribute('data-tip'), cut = text.indexOf(' · ');
        var html = cut > 0 ? '<b>' + esc(text.slice(0, cut)) + '</b>' + esc(text.slice(cut + 3)) : esc(text);
        readTip(wrap, tip, html, e.clientX - r.left, r.width);
        return;
      }
      if (!svg || !readLines(wrap, svg, tip, e)) readOff(wrap);
    });
    d.addEventListener('pointerleave', function () { readOff(reading); reading = null; });
    [].forEach.call(d.querySelectorAll('svg.curve[data-editable]'), function (svg) {
      var D = svg.dataset, X0 = +D.xMin, X1 = +D.xMax, Y0 = +D.yMin, Y1 = +D.yMax, step = +(D.step || 1);
      var box = svg.querySelector('.frame').getBBox(), line = svg.querySelector('.line');
      var dots = [].slice.call(svg.querySelectorAll('.pt')), cur = null;
      function pos(p) { return [box.x + (p.x - X0) / (X1 - X0) * box.width, box.y + box.height - (p.y - Y0) / (Y1 - Y0) * box.height]; }
      function val(dot) { return { x: +dot.dataset.x, y: +dot.dataset.y }; }
      function draw() {
        var p = dots.map(function (dt) { return pos(val(dt)); });
        dots.forEach(function (dt, i) { dt.setAttribute('x', p[i][0] - 5); dt.setAttribute('y', p[i][1] - 5); dt.setAttribute('aria-valuenow', dt.dataset.y); dt.setAttribute('aria-valuetext', dt.dataset.x + ' → ' + dt.dataset.y); });
        var all = [[box.x, p[0][1]]].concat(p, [[box.x + box.width, p[p.length - 1][1]]]);
        line.setAttribute('points', all.map(function (q) { return q.join(','); }).join(' '));
      }
      function put(i, x, y) {
        var lo = i > 0 ? +dots[i - 1].dataset.x + step : X0, hi = i < dots.length - 1 ? +dots[i + 1].dataset.x - step : X1;
        var ylo = i > 0 ? +dots[i - 1].dataset.y : Y0, yhi = i < dots.length - 1 ? +dots[i + 1].dataset.y : Y1;
        var sn = function (v) { return Math.round(v / step) * step; };
        dots[i].dataset.x = Math.max(lo, Math.min(hi, sn(x)));
        dots[i].dataset.y = Math.max(ylo, Math.min(yhi, sn(y)));
        draw();
        svg.dispatchEvent(new CustomEvent('change', { detail: dots.map(val) }));
      }
      dots.forEach(function (dt, i) {
        dt.setAttribute('tabindex', 0); dt.setAttribute('role', 'slider');
        dt.addEventListener('pointerdown', function (e) { cur = i; dt.setPointerCapture(e.pointerId); dt.focus(); e.preventDefault(); });
        dt.addEventListener('pointermove', function (e) {
          if (cur !== i) return;
          var r = svg.getBoundingClientRect(), vb = svg.viewBox.baseVal;
          var sx = (e.clientX - r.left) / r.width * vb.width + vb.x, sy = (e.clientY - r.top) / r.height * vb.height + vb.y;
          put(i, X0 + (sx - box.x) / box.width * (X1 - X0), Y0 + (box.y + box.height - sy) / box.height * (Y1 - Y0));
        });
        dt.addEventListener('pointerup', function () { cur = null; });
        dt.addEventListener('keydown', function (e) {
          var v = val(dt), k = e.key, dx = k === 'ArrowRight' ? step : k === 'ArrowLeft' ? -step : 0, dy = k === 'ArrowUp' ? step : k === 'ArrowDown' ? -step : 0;
          if (!dx && !dy) return;
          e.preventDefault(); put(i, v.x + dx, v.y + dy);
        });
      });
      draw();
    });

    // ── Live data API (window.Kante) ─────────────────────────────────────
    // tick(el, text)   a value changed: it counts up to the new number and a cyan strip fades
    // fresh(el)        new data arrived: a 2px line runs along the bottom edge of the tile
    // stale(el, on, t) data is old: the tile dims, warning stripes sit on its bottom edge, t is the age
    // edit(box, on)    edit mode: tier bars turn cyan, brackets appear one after the other
    // settle(el, from) after a drop: the element glides from `from` (a DOMRect) into place with a small overshoot
    var K = window.Kante = window.Kante || {};
    var moving = h.classList.contains('motion');
    // restart replays a class animation. The class comes back two frames later, for all elements
    // restarted meanwhile at once: a style pass without it lies in between, and nothing forces a
    // layout (void el.offsetWidth per element cost ~20 ms each, 230 ms for 11 tiles on a big page).
    var replay = [];
    function restart(el, cls, ms) {
      el.classList.remove(cls);
      if (!replay.length) {
        requestAnimationFrame(function () {
          requestAnimationFrame(function () {
            var due = replay;
            replay = [];
            due.forEach(function (r) {
              r.el.classList.add(r.cls);
              setTimeout(function () { r.el.classList.remove(r.cls); }, r.ms);
            });
          });
        });
      }
      replay.push({ el: el, cls: cls, ms: ms });
    }
    function lineOf(el) {
      var l = el.querySelector(':scope > .live-line');
      if (!l) {
        l = d.createElement('span');
        l.className = 'live-line';
        l.setAttribute('aria-hidden', 'true');
        el.appendChild(l);
      }
      return l;
    }
    K.fresh = function (el) {
      if (!moving || !el) return;
      el.setAttribute('data-live-tile', '');
      lineOf(el);
      restart(el, 'is-fresh', 950);
    };
    K.stale = function (el, on, text) {
      if (!el) return;
      el.setAttribute('data-live-tile', '');
      var l = lineOf(el);
      if (text !== undefined) l.setAttribute('data-text', text);
      el.classList.toggle('is-stale', !!on);
    };
    var counting = false;
    function ints(a, b) {
      var re = /^\D*\d[\d\s\u00a0.,]*\D*$/;
      return re.test(a) && re.test(b) && !/[.,]\d{1,2}(\D|$)/.test(a + b);
    }
    K.tick = function (el, text) {
      if (!el) return;
      var from = el.textContent, to = text === undefined ? from : text;
      if (text !== undefined && text === from) return;
      if (moving) restart(el, 'is-changed', 1150);
      if (!moving || from === to || !ints(from, to)) { if (text !== undefined) el.textContent = to; return; }
      var a = parseInt(from.replace(/\D/g, ''), 10), b = parseInt(to.replace(/\D/g, ''), 10);
      var sep = (/\d([\s\u00a0.,])\d/.exec(to) || [])[1] || '';
      var pre = /^\D*/.exec(to)[0], post = /\D*$/.exec(to)[0];
      var n = 0, steps = 14;
      counting = true;
      var t = setInterval(function () {
        n++;
        var v = Math.round(a + (b - a) * (1 - Math.pow(1 - n / steps, 3)));
        var s = String(v).replace(/\B(?=(\d{3})+(?!\d))/g, sep === '' ? '' : sep);
        el.textContent = n >= steps ? to : pre + s + post;
        if (n >= steps) { clearInterval(t); setTimeout(function () { counting = false; }, 0); }
      }, 45);
    };
    K.edit = function (box, on) {
      if (!box) return;
      [].forEach.call(box.children, function (c, i) { c.style.setProperty('--i', i % 12); });
      box.classList.toggle('is-editing', on === undefined ? !box.classList.contains('is-editing') : !!on);
    };
    K.settle = function (el, from) {
      if (!moving || !el || !from || !el.animate) return;
      var to = el.getBoundingClientRect();
      var dx = from.left - to.left, dy = from.top - to.top;
      if (!dx && !dy) return;
      el.animate([{ transform: 'translate(' + dx + 'px,' + dy + 'px)' }, { transform: 'none' }],
                 { duration: 380, easing: 'cubic-bezier(.3,1.5,.5,1)' });
    };
    // Automatic: [data-live] values react to text changes, [data-live-tile] surfaces to a settled htmx swap.
    if ('MutationObserver' in window) {
      var seen = new WeakMap();
      [].forEach.call(d.querySelectorAll('[data-live]'), function (e) { seen.set(e, e.textContent); });
      new MutationObserver(function (list) {
        if (counting) return;
        list.forEach(function (m) {
          var e = m.target.nodeType === 1 ? m.target : m.target.parentNode;
          e = e && e.closest && e.closest('[data-live]');
          if (!e) return;
          var was = seen.get(e), now = e.textContent;
          seen.set(e, now);
          if (was !== undefined && was !== now && moving) { restart(e, 'is-changed', 1150); }
        });
      }).observe(d.body, { subtree: true, childList: true, characterData: true });
    }
    d.addEventListener('htmx:afterSettle', function (e) {
      var t = e.target;
      if (!t || !t.querySelectorAll) return;
      if (t.hasAttribute && t.hasAttribute('data-live-tile')) K.fresh(t);
      [].forEach.call(t.querySelectorAll('[data-live-tile]'), K.fresh);
    });

    // ── Fold state (Kante 1.8) ───────────────────────────────────────────
    // details.fold[data-key]: data-open ("true" / "false", written by the server) sets the fold at
    // load and after an htmx swap. Every open or close by the user fires "kante:fold" (bubbles)
    // with detail {key, open}, so the app can keep it on the server:
    //   document.addEventListener('kante:fold', function (e) { post('/fold', e.detail); });
    var FOLD = 'details.fold[data-key]';
    K.folds = function (root) {
      root = root || d;
      var all = [].slice.call(root.querySelectorAll ? root.querySelectorAll(FOLD) : []);
      if (root.matches && root.matches(FOLD)) all.push(root);
      all.forEach(function (f) {
        if (f.dataset.open === undefined) { f.dataset.open = String(f.open); return; }
        f.open = f.dataset.open === 'true';
      });
    };
    K.folds();
    d.addEventListener('htmx:afterSettle', function (e) { if (e.target) K.folds(e.target); });
    // toggle does not bubble: listen while capturing. A toggle that matches data-open was ours.
    d.addEventListener('toggle', function (e) {
      var f = e.target;
      if (!f.matches || !f.matches(FOLD)) return;
      var open = String(f.open);
      if (f.dataset.open === undefined || f.dataset.open === open) { f.dataset.open = open; return; }
      f.dataset.open = open;
      f.dispatchEvent(new CustomEvent('kante:fold', { bubbles: true, detail: { key: f.dataset.key, open: f.open } }));
    }, true);

    if (!h.classList.contains('motion')) return;

    // Counter (A06): figures in the facts strip roll up digit by digit.
    [].forEach.call(d.querySelectorAll('.fact b'), function (b) {
      var m = /^(\D*)(\d+)(\D*)$/.exec(b.textContent.trim());
      if (!m) return;
      b.setAttribute('aria-label', b.textContent.trim());
      b.textContent = m[1];
      m[2].split('').forEach(function (ch) {
        var col = d.createElement('i'), roll = d.createElement('i');
        col.className = 'odo-d';
        roll.className = 'odo-r';
        col.setAttribute('aria-hidden', 'true');
        for (var i = 0; i <= 9; i++) { var x = d.createElement('i'); x.textContent = i; roll.appendChild(x); }
        col.appendChild(roll);
        col.dataset.v = ch;
        b.appendChild(col);
      });
      if (m[3]) b.appendChild(d.createTextNode(m[3]));
      b.classList.add('odo');
    });

    // Teleprinter (A07): [data-type] labels type in with a block cursor.
    [].forEach.call(d.querySelectorAll('[data-type]'), function (el) {
      el.dataset.text = el.textContent;
      el.style.minHeight = el.offsetHeight + 'px';
      el.textContent = '';
    });

    // Entrances (A04 scan, A05 bar, A17 cascade) and the two above, once in view.
    var targets = d.querySelectorAll('.feat,.tiles>.tile,.hero-shot,.showcase figure,.shots figure,.fact b.odo,[data-type]');
    [].forEach.call(d.querySelectorAll('.features,.tiles'), function (g) {
      [].forEach.call(g.children, function (c, i) { c.style.setProperty('--i', i % 12); });
    });
    var io = new IntersectionObserver(function (entries) {
      entries.forEach(function (en) {
        if (!en.isIntersecting) return;
        var el = en.target;
        io.unobserve(el);
        el.classList.add('is-in');
        if (el.classList.contains('odo')) {
          [].forEach.call(el.querySelectorAll('.odo-d'), function (col, i) {
            var roll = col.firstChild;
            roll.style.transitionDelay = (i * 120) + 'ms';
            requestAnimationFrame(function () { roll.style.transform = 'translateY(-' + col.dataset.v + 'em)'; });
          });
        }
        if (el.dataset.text !== undefined) {
          var text = el.dataset.text, n = 0;
          el.classList.add('is-typing');
          var t = setInterval(function () {
            el.textContent = text.slice(0, ++n);
            if (n >= text.length) { clearInterval(t); setTimeout(function () { el.classList.remove('is-typing'); }, 2000); }
          }, 55);
        }
      });
    }, { rootMargin: '0px 0px -8% 0px' });
    [].forEach.call(targets, function (el) {
      if (el.classList.contains('feat') || el.matches('.tiles>.tile,.hero-shot,.showcase figure,.shots figure')) el.setAttribute('data-entrance', '');
      io.observe(el);
    });
  });
})();
