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

  document.addEventListener("DOMContentLoaded", function () { logo(); entrances(); bars(); counters(); copy(); });
})();
