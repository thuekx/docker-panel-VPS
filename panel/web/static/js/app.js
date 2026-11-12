/* Nostek Panel UI motion: Motion (motion.dev) for entrances, anime.js for the logo and counters.
   Everything is decoration only; the page works without JavaScript and respects reduced motion. */
(function () {
  "use strict";
  var reduce = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  function all(sel) { return Array.prototype.slice.call(document.querySelectorAll(sel)); }

  function bars() {
    all("[data-bar]").forEach(function (el) {
      var v = Math.max(0, Math.min(100, parseInt(el.getAttribute("data-bar"), 10) || 0));
      if (reduce || !window.anime) { el.style.width = v + "%"; return; }
      window.anime.animate(el, { width: ["0%", v + "%"], duration: 1100, delay: 250, ease: "outExpo" });
    });
  }

  function counters() {
    if (reduce || !window.anime) return;
    all("[data-count]").forEach(function (el) {
      var target = parseInt(el.getAttribute("data-count"), 10) || 0;
      var o = { v: 0 };
      window.anime.animate(o, { v: target, duration: 1100, delay: 200, ease: "outExpo",
        onUpdate: function () { el.textContent = Math.round(o.v); } });
    });
  }

  function logo() {
    if (reduce || !window.anime) return;
    var parts = all(".mark .m-part");
    if (!parts.length) return;
    window.anime.animate(parts, {
      opacity: [0, 1],
      translateY: [-8, 0],
      scale: [0.92, 1],
      delay: window.anime.stagger(90),
      duration: 700,
      ease: "outBack(1.6)"
    });
  }

  function entrances() {
    if (reduce || !window.Motion) return;
    var animate = window.Motion.animate, stagger = window.Motion.stagger;
    var cards = all("[data-anim=card]");
    if (cards.length) animate(cards, { opacity: [0, 1], y: [18, 0], filter: ["blur(6px)", "blur(0px)"] }, { duration: 0.55, delay: stagger(0.07), ease: [0.22, 1, 0.36, 1] });
    var nav = all("[data-anim=nav]");
    if (nav.length) animate(nav, { opacity: [0, 1], x: [-12, 0] }, { duration: 0.4, delay: stagger(0.05) });
    var items = all("[data-anim=item]");
    if (items.length) animate(items, { opacity: [0, 1], y: [6, 0] }, { duration: 0.3, delay: stagger(0.025, { startDelay: 0.25 }) });
    var pop = all("[data-anim=pop]");
    if (pop.length) animate(pop, { opacity: [0, 1], scale: [0.9, 1] }, { type: "spring", bounce: 0.35, duration: 0.6 });
    var t = all("[data-anim=title]");
    if (t.length) animate(t, { opacity: [0, 1], y: [8, 0] }, { duration: 0.45 });
    var shake = all("[data-anim=shake]");
    if (shake.length) animate(shake, { x: [0, -8, 8, -5, 5, 0] }, { duration: 0.45 });
  }

  function copy() {
    all("[data-copy]").forEach(function (b) {
      b.addEventListener("click", function () {
        var el = document.getElementById(b.getAttribute("data-copy"));
        if (el && navigator.clipboard) navigator.clipboard.writeText(el.textContent);
      });
    });
  }

  // A running job page follows its progress without reloading: it polls the job status (JSON),
  // moves the progress bar and the steps, and swaps in the finished card once when the job ends.
  function job() {
    var card = document.querySelector("[data-job]");
    if (!card) return;
    var url = card.getAttribute("data-job"), started = Date.now(), fails = 0, timer = null;
    var bar = card.querySelector(".progress i"), meter = card.querySelector(".progress"), clock = card.querySelector("[data-elapsed]");
    function tick() {
      if (!clock) return;
      var s = Math.floor((Date.now() - started) / 1000);
      clock.textContent = Math.floor(s / 60) + ":" + ("0" + (s % 60)).slice(-2);
    }
    var clockTimer = setInterval(tick, 1000); tick();
    function setBar(p) {
      if (!bar) return;
      bar.removeAttribute("data-pct");
      bar.style.width = Math.max(6, p) + "%";
      if (meter) meter.setAttribute("aria-valuenow", String(p));
    }
    function setSteps(steps) {
      steps.forEach(function (st) {
        var li = card.querySelector('[data-step="' + (window.CSS && CSS.escape ? CSS.escape(st.key) : st.key) + '"]');
        if (!li || li.classList.contains("st-" + st.state)) return;
        li.className = "st-" + st.state;
        if (!reduce && window.Motion && (st.state === "done" || st.state === "active" || st.state === "failed")) {
          window.Motion.animate(li.querySelector(".dot"), { scale: [0.6, 1.15, 1] }, { duration: 0.5, ease: [0.34, 1.56, 0.64, 1] });
          window.Motion.animate(li.querySelector(".txt"), { opacity: [0.3, 1], x: [-6, 0] }, { duration: 0.35 });
        }
      });
    }
    function finish() {
      clearInterval(clockTimer);
      if (card.hasAttribute("data-job-reload")) { location.reload(); return; } // the page lists what the job made
      fetch(location.href, { credentials: "same-origin", cache: "no-store" }).then(function (r) { return r.text(); }).then(function (html) {
        var next = new DOMParser().parseFromString(html, "text/html").getElementById("job");
        if (!next) { location.reload(); return; }
        card.replaceWith(next);
        copy();
        if (!reduce && window.Motion) {
          var parts = next.querySelectorAll(".job-done, [data-anim=pop]");
          if (parts.length) window.Motion.animate(parts, { opacity: [0, 1], y: [12, 0], scale: [0.97, 1] }, { duration: 0.5, delay: window.Motion.stagger(0.12), ease: [0.22, 1, 0.36, 1] });
        }
      }).catch(function () { location.reload(); });
    }
    function poll() {
      fetch(url, { credentials: "same-origin", cache: "no-store", headers: { Accept: "application/json" } })
        .then(function (r) { if (!r.ok) throw new Error(String(r.status)); return r.json(); })
        .then(function (d) {
          fails = 0;
          setSteps(d.steps || []);
          setBar(d.percent || 0);
          if (d.state === "running") { timer = setTimeout(poll, 1500); return; }
          setBar(d.state === "done" ? 100 : (d.percent || 0));
          setTimeout(finish, d.state === "done" ? 700 : 300);
        })
        .catch(function () {
          fails++;
          if (fails > 5) { setTimeout(function () { location.reload(); }, 5000); return; }
          timer = setTimeout(poll, 1500 * fails);
        });
    }
    timer = setTimeout(poll, 800);
  }

  // A site page follows its containers: CPU, memory, uptime and restarts update every 5 seconds while the
  // page is visible. When a container changes state the stack card is swapped in once from the page.
  function stack() {
    var card = document.querySelector("[data-stack]");
    if (!card) return;
    var url = card.getAttribute("data-stack"), live = card.querySelector("[data-live]"), fails = 0;
    function set(el, f, v) {
      var x = el.querySelector('[data-f="' + f + '"]');
      if (!x) return;
      if (f.slice(-4) === "_pct") {
        var w = Math.max(0, Math.min(100, v)) + "%";
        if (reduce || !window.anime) x.style.width = w; else window.anime.animate(x, { width: w, duration: 700, ease: "outExpo" });
        return;
      }
      if (x.textContent !== String(v)) {
        x.textContent = v;
        if (!reduce && window.Motion) window.Motion.animate(x, { opacity: [0.35, 1] }, { duration: 0.4 });
      }
    }
    function swap() {
      fetch(location.href, { credentials: "same-origin", cache: "no-store" }).then(function (r) { return r.text(); }).then(function (html) {
        var next = new DOMParser().parseFromString(html, "text/html").getElementById("stack");
        if (!next) return;
        card.replaceWith(next); card = next; live = card.querySelector("[data-live]");
        all("#stack [data-bar]").forEach(function (el) { el.style.width = (parseInt(el.getAttribute("data-bar"), 10) || 0) + "%"; });
        if (live) live.hidden = false;
      });
    }
    function poll() {
      if (document.hidden) { setTimeout(poll, 5000); return; }
      fetch(url, { credentials: "same-origin", cache: "no-store", headers: { Accept: "application/json" } })
        .then(function (r) { if (!r.ok) throw new Error(String(r.status)); return r.json(); })
        .then(function (d) {
          fails = 0;
          if (live) live.hidden = false;
          card.classList.remove("stale");
          var changed = false, seen = 0;
          (d.containers || []).forEach(function (c) {
            var el = card.querySelector('[data-ctr="' + c.service + '"]');
            if (!el) { changed = true; return; }
            seen++;
            if (el.getAttribute("data-state") !== c.state + "/" + c.health) { changed = true; return; }
            set(el, "cpu", c.cpu); set(el, "mem", c.mem); set(el, "since", c.since); set(el, "restarts", c.restarts);
            set(el, "cpu_pct", c.cpu_pct); set(el, "mem_pct", c.mem_pct);
          });
          if (changed || seen !== card.querySelectorAll("[data-ctr]").length) swap();
        })
        .catch(function () { fails++; if (live) live.hidden = true; if (fails > 1) card.classList.add("stale"); })
        .then(function () { setTimeout(poll, fails > 3 ? 20000 : 5000); });
    }
    setTimeout(poll, 3000);
  }

  // asks before a destructive button (the action still works without JavaScript: deletes go to a 7-day trash)
  function confirms() {
    document.addEventListener("click", function (e) {
      var b = e.target.closest && e.target.closest("[data-confirm]");
      if (b && !window.confirm(b.getAttribute("data-confirm"))) e.preventDefault();
    });
  }

  // upload forms show real progress; the server's redirect decides the next page
  function uploads() {
    all("form[data-upload]").forEach(function (f) {
      f.addEventListener("submit", function (e) {
        if (!window.FormData || !window.XMLHttpRequest) return;
        e.preventDefault();
        var bar = f.querySelector("[data-upload-bar]"), fill = bar && bar.querySelector("i");
        var btn = f.querySelector("button[type=submit]");
        if (bar) bar.hidden = false;
        if (btn) btn.disabled = true;
        var x = new XMLHttpRequest();
        x.open("POST", f.action);
        x.upload.onprogress = function (ev) {
          if (fill && ev.lengthComputable) fill.style.width = Math.round(ev.loaded * 100 / ev.total) + "%";
        };
        x.onload = function () { window.location.assign(x.responseURL || window.location.href); };
        x.onerror = function () { if (btn) btn.disabled = false; if (bar) bar.classList.add("failed"); };
        x.send(new FormData(f));
      });
    });
  }

  // the file editor: Tab inserts a tab, Ctrl/Cmd+S saves, leaving with unsaved changes asks first
  function editor() {
    var f = document.querySelector("form[data-editor]");
    if (!f) return;
    var ta = f.querySelector("textarea"), flag = f.querySelector("[data-dirty]"), dirty = false;
    ta.addEventListener("input", function () { dirty = true; if (flag) flag.hidden = false; });
    ta.addEventListener("keydown", function (e) {
      if (e.key === "Tab" && !e.shiftKey && !e.ctrlKey && !e.metaKey && !e.altKey) {
        e.preventDefault();
        var a = ta.selectionStart, b = ta.selectionEnd;
        ta.value = ta.value.slice(0, a) + "\t" + ta.value.slice(b);
        ta.selectionStart = ta.selectionEnd = a + 1;
        dirty = true; if (flag) flag.hidden = false;
      }
      if ((e.ctrlKey || e.metaKey) && e.key === "s") { e.preventDefault(); dirty = false; f.requestSubmit ? f.requestSubmit() : f.submit(); }
    });
    f.addEventListener("submit", function () { dirty = false; });
    window.addEventListener("beforeunload", function (e) { if (dirty) { e.preventDefault(); e.returnValue = ""; } });
  }

  // one open ⋯ menu at a time
  function menus() {
    document.addEventListener("click", function (e) {
      all("details.menu[open]").forEach(function (d) { if (!d.contains(e.target)) d.open = false; });
    });
  }

  // long WordPress actions: the button says it is working and cannot be pressed twice
  function busy() {
    document.addEventListener("submit", function (e) {
      var b = e.submitter || (e.target.querySelector && e.target.querySelector("button[data-busy]"));
      if (!b || !b.hasAttribute("data-busy") || e.defaultPrevented) return;
      setTimeout(function () { b.disabled = true; b.classList.add("busy"); b.textContent = b.getAttribute("data-busy"); }, 0);
    });
  }

  // the one-click login page opens wp-admin by itself (a link the browser follows, so form-action stays 'self')
  function go() {
    var a = document.querySelector("a[data-go]");
    if (a) setTimeout(function () { window.location.replace(a.href); }, 300);
  }

  document.addEventListener("DOMContentLoaded", function () { logo(); entrances(); bars(); counters(); copy(); job(); stack(); confirms(); uploads(); editor(); menus(); busy(); go(); });
})();
