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

  // Copy buttons: the text is copied, the button turns into a check mark with "Copied" for 2 s, and a small
  // inline notice (announced to screen readers) confirms it next to the button. A failed copy says so and selects
  // the text, so it can be copied by hand.
  function copy() {
    var body = document.body;
    var okText = body.getAttribute("data-copied") || "Copied", failText = body.getAttribute("data-copyfail") || "Copy failed";
    function fallback(text) {
      var ta = document.createElement("textarea");
      ta.value = text; ta.setAttribute("readonly", ""); ta.style.position = "fixed"; ta.style.opacity = "0";
      body.appendChild(ta); ta.select();
      var ok = false;
      try { ok = document.execCommand("copy"); } catch (e) { ok = false; }
      body.removeChild(ta);
      return ok ? Promise.resolve() : Promise.reject(new Error("copy"));
    }
    function selectText(el) {
      var r = document.createRange(); r.selectNodeContents(el);
      var sel = window.getSelection(); sel.removeAllRanges(); sel.addRange(r);
    }
    function notice(b, text, good) {
      var n = b.parentNode.querySelector(".copy-note");
      if (!n) {
        n = document.createElement("span");
        n.className = "copy-note"; n.setAttribute("role", "status"); n.setAttribute("aria-live", "polite");
        b.insertAdjacentElement("afterend", n);
      }
      n.textContent = text; n.classList.toggle("bad", !good); n.classList.add("on");
      if (!reduce && window.Motion) window.Motion.animate(n, { opacity: [0, 1], y: [6, 0], scale: [0.92, 1] }, { type: "spring", bounce: 0.4, duration: 0.45 });
      clearTimeout(n._t);
      n._t = setTimeout(function () { n.classList.remove("on"); }, 2200);
    }
    all("[data-copy]").forEach(function (b) {
      var label = b.textContent;
      b.addEventListener("click", function () {
        var el = document.getElementById(b.getAttribute("data-copy"));
        if (!el) return;
        var text = el.textContent;
        var p = navigator.clipboard && window.isSecureContext ? navigator.clipboard.writeText(text).catch(function () { return fallback(text); }) : fallback(text);
        p.then(function () {
          b.classList.add("copied"); b.textContent = "\u2713 " + okText;
          if (!reduce && window.Motion) window.Motion.animate(b, { scale: [1, 0.92, 1.06, 1] }, { duration: 0.4 });
          notice(b, okText, true);
          clearTimeout(b._t);
          b._t = setTimeout(function () { b.classList.remove("copied"); b.textContent = label; }, 2000);
        }, function () {
          selectText(el);
          notice(b, failText, false);
        });
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
      var rows = card.querySelectorAll("[data-step]");
      if (steps.length && rows.length !== steps.length) { location.reload(); return; } // the list of steps changed
      steps.forEach(function (st) {
        var li = card.querySelector('[data-step="' + (window.CSS && CSS.escape ? CSS.escape(st.key) : st.key) + '"]');
        if (!li) { location.reload(); return; }
        if (li.classList.contains("st-" + st.state)) return;
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
          if (d.state === "running") setSteps(d.steps || []); // a finished job swaps in its own card
          var pr = card.querySelector("[data-progress]");
          if (pr) pr.textContent = d.progress || "";
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
    var mark = function () { dirty = true; if (flag) flag.hidden = false; };
    var save = function () { dirty = false; f.requestSubmit ? f.requestSubmit() : f.submit(); };
    // EDITOR-1: CodeMirror 6 (vendor/codemirror.js, loaded deferred by this page); the textarea stays the form field
    // and the fallback when the bundle is missing
    if (window.NostekEditor) {
      try {
        var ed = window.NostekEditor.mount(ta, {name: ta.getAttribute("data-name"), nonce: ta.getAttribute("data-nonce"), onChange: mark, onSave: save});
        var bar = f.querySelector("[data-editor-bar]"), card = f.closest(".card");
        if (bar) {
          bar.hidden = false;
          var l = bar.querySelector("[data-ed-lang]"); if (l) l.textContent = ed.lang;
          bar.addEventListener("click", function (e) {
            var b = e.target.closest("button[data-ed]"); if (!b) return;
            var what = b.getAttribute("data-ed");
            if (what === "search") ed.search();
            if (what === "wrap") b.setAttribute("aria-pressed", String(ed.toggleWrap()));
            if (what === "full" && card) {
              // the browser's own full screen (Esc leaves it); the card fills it
              if (document.fullscreenElement) document.exitFullscreen();
              else if (card.requestFullscreen) card.requestFullscreen().catch(function () {});
            }
            ed.focus();
          });
          document.addEventListener("fullscreenchange", function () {
            var on = document.fullscreenElement === card, fb = bar.querySelector('[data-ed="full"]');
            card.classList.toggle("ed-full", on); if (fb) fb.setAttribute("aria-pressed", String(on));
          });
          var fbtn = bar.querySelector('[data-ed="full"]');
          if (fbtn && !(card && card.requestFullscreen)) fbtn.hidden = true;
        }
        if (window.matchMedia) window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", function () { ed.syncTheme(); });
        f.addEventListener("submit", function () { dirty = false; });
        window.addEventListener("beforeunload", function (e) { if (dirty) { e.preventDefault(); e.returnValue = ""; } });
        return;
      } catch (err) { ta.hidden = false; }
    }
    ta.addEventListener("input", mark);
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

  // long actions (WordPress Tools, start/stop/restart): the button spins and says what runs, every button of the
  // form waits, and a note under it says how long it may take; the page that answers shows the result
  function busy() {
    document.addEventListener("submit", function (e) {
      var f = e.target, b = e.submitter || (f.querySelector && f.querySelector("button[data-busy]"));
      if (!b || !b.hasAttribute("data-busy") || e.defaultPrevented) return;
      setTimeout(function () {
        f.querySelectorAll("button").forEach(function (x) { x.disabled = true; });
        b.classList.add("busy"); b.textContent = b.getAttribute("data-busy");
        var note = f.querySelector(".busy-note") || (f.parentNode && f.parentNode.querySelector(".busy-note"));
        if (note) note.hidden = false;
      }, 0);
    });
  }

  // the one-click login page opens wp-admin by itself (a link the browser follows, so form-action stays 'self')
  function go() {
    var a = document.querySelector("a[data-go]");
    if (a) setTimeout(function () { window.location.replace(a.href); }, 300);
  }

  // the result of a save, delete or other action also shows as a small note at the bottom right for a few seconds,
  // so it is seen without scrolling back up; the banner at the top stays (and is the one screen readers announce)
  function toasts() {
    var box = document.getElementById("toasts");
    if (!box) return;
    var close = document.body.getAttribute("data-toastclose") || "Close";
    all("[data-toast]").slice(0, 3).forEach(function (src) {
      var text = (src.textContent || "").replace(/\s+/g, " ").trim();
      if (!text) return;
      var kind = src.classList.contains("err") ? "err" : src.classList.contains("warn") ? "warn" : "ok";
      var t = document.createElement("div");
      t.className = "toast toast-" + kind;
      var ic = document.createElement("span"); ic.className = "toast-ic"; ic.setAttribute("aria-hidden", "true");
      ic.textContent = kind === "ok" ? "\u2705" : "\u26A0\uFE0F";
      var msg = document.createElement("span"); msg.className = "toast-msg"; msg.textContent = text;
      var x = document.createElement("button"); x.type = "button"; x.className = "toast-x";
      x.setAttribute("aria-label", close); x.title = close; x.textContent = "\u00D7";
      t.appendChild(ic); t.appendChild(msg); t.appendChild(x);
      box.appendChild(t);
      var timer = null, gone = false;
      function hide() {
        if (gone) return; gone = true; clearTimeout(timer);
        if (reduce) { t.remove(); return; }
        t.classList.add("toast-out");
        setTimeout(function () { t.remove(); }, 260);
      }
      function arm() { clearTimeout(timer); timer = setTimeout(hide, kind === "ok" ? 5000 : 9000); }
      x.addEventListener("click", hide);
      t.addEventListener("mouseenter", function () { clearTimeout(timer); });
      t.addEventListener("mouseleave", arm);
      t.addEventListener("focusin", function () { clearTimeout(timer); });
      t.addEventListener("focusout", arm);
      if (!reduce) requestAnimationFrame(function () { t.classList.add("toast-in"); }); else t.classList.add("toast-in");
      arm();
    });
  }


  // passkeys (AUTH-02): add one on the profile page, sign in with one on the login page
  function passkeys() {
    var ok = !!(window.PublicKeyCredential && navigator.credentials);
    all("[data-passkey-none]").forEach(function (x) { x.hidden = ok; });
    if (!ok) return;
    function b64(buf) {
      var s = "", b = new Uint8Array(buf);
      for (var i = 0; i < b.length; i++) s += String.fromCharCode(b[i]);
      return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
    }
    function unb64(s) {
      s = s.replace(/-/g, "+").replace(/_/g, "/"); while (s.length % 4) s += "=";
      var r = atob(s), b = new Uint8Array(r.length);
      for (var i = 0; i < r.length; i++) b[i] = r.charCodeAt(i);
      return b.buffer;
    }
    function post(url, fields) {
      var body = new URLSearchParams();
      Object.keys(fields).forEach(function (k) { body.append(k, fields[k]); });
      return fetch(url, { method: "POST", body: body, credentials: "same-origin", headers: { "Accept": "application/json" } })
        .then(function (r) { return r.json().then(function (j) { if (!r.ok) throw new Error(j.error || r.status); return j; }); });
    }
    function show(box, msg) { var m = box.querySelector("[data-pk-msg]"); if (m) { m.textContent = msg; m.hidden = !msg; } }
    function done(btn, label) { btn.disabled = false; btn.classList.remove("busy"); btn.textContent = label; }
    all("[data-passkey-add]").forEach(function (f) {
      f.hidden = false;
      var btn = f.querySelector("button"), label = btn.textContent;
      f.addEventListener("submit", function (e) {
        e.preventDefault(); show(f, "");
        btn.disabled = true; btn.classList.add("busy"); btn.textContent = btn.getAttribute("data-busy");
        var csrf = f.getAttribute("data-csrf");
        post("/profile/passkeys/begin", { csrf: csrf, password: f.elements.password.value }).then(function (o) {
          o.challenge = unb64(o.challenge); o.user.id = unb64(o.user.id);
          o.excludeCredentials.forEach(function (c) { c.id = unb64(c.id); });
          return navigator.credentials.create({ publicKey: o });
        }).then(function (c) {
          return post("/profile/passkeys/finish", { csrf: csrf, name: f.elements.name.value,
            client: b64(c.response.clientDataJSON), att: b64(c.response.attestationObject) });
        }).then(function (j) { window.location.assign(j.go); })
          .catch(function (err) { done(btn, label); show(f, err && err.name === "NotAllowedError" ? "" : String(err.message || err)); });
      });
    });
    all("[data-passkey-login]").forEach(function (box) {
      box.hidden = false;
      var btn = box.querySelector("button"), label = btn.textContent, csrf = box.getAttribute("data-csrf");
      btn.addEventListener("click", function () {
        show(box, ""); btn.disabled = true; btn.classList.add("busy"); btn.textContent = btn.getAttribute("data-busy");
        post("/login/passkey/begin", { csrf: csrf }).then(function (o) {
          o.challenge = unb64(o.challenge);
          return navigator.credentials.get({ publicKey: o });
        }).then(function (c) {
          var r = c.response;
          return post("/login/passkey/finish", { csrf: csrf, id: b64(c.rawId), client: b64(r.clientDataJSON),
            auth: b64(r.authenticatorData), sig: b64(r.signature), user: r.userHandle ? b64(r.userHandle) : "" });
        }).then(function (j) { window.location.assign(j.go); })
          .catch(function (err) { done(btn, label); show(box, err && err.name === "NotAllowedError" ? "" : String(err.message || err)); });
      });
    });
  }

  // UPD-01: a checkbox with data-reveal shows its fields (and makes them required) only while it is ticked.
  function reveal() {
    all("[data-reveal]").forEach(function (box) {
      var part = document.querySelector(box.getAttribute("data-reveal"));
      if (!part) return;
      function set() {
        part.hidden = !box.checked;
        part.querySelectorAll("input").forEach(function (i) { i.required = box.checked; });
      }
      box.addEventListener("change", set); set();
    });
  }

  // UPD-01: the update card polls the run's status. While the panel restarts the answer fails for a while; the
  // card says so and keeps asking, then reloads once the run has ended.
  function updater() {
    var card = document.querySelector("[data-update]");
    if (!card || !card.getAttribute("data-busy-run")) return;
    var url = card.getAttribute("data-update"), lines = card.querySelector("[data-upd-lines]"), note = card.querySelector("[data-upd-note]");
    var fails = 0;
    function poll() {
      fetch(url, { credentials: "same-origin", cache: "no-store", headers: { Accept: "application/json" } })
        .then(function (r) { if (!r.ok) throw new Error(String(r.status)); return r.json(); })
        .then(function (d) {
          fails = 0;
          if (note) note.textContent = "";
          if (lines && d.lines) { lines.textContent = d.lines.join("\n") + "\n"; lines.scrollTop = lines.scrollHeight; }
          if (!d.busy) { location.reload(); return; }
          setTimeout(poll, 3000);
        })
        .catch(function () {
          fails++;
          if (note) note.textContent = card.getAttribute("data-restarting");
          setTimeout(poll, Math.min(10000, 3000 + fails * 1000));
        });
    }
    setTimeout(poll, 2000);
  }

  document.addEventListener("DOMContentLoaded", function () { logo(); entrances(); bars(); counters(); copy(); job(); stack(); confirms(); uploads(); editor(); menus(); busy(); go(); passkeys(); reveal(); updater(); toasts(); });
})();
