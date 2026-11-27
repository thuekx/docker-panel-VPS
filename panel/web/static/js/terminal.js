// TERM-01: the web terminal of a website (xterm.js). The socket's first message carries the CSRF token and the
// size; after that, binary messages are the terminal's bytes and text messages resize it.
(function () {
  'use strict';
  var box = document.getElementById('term');
  if (!box || !window.Terminal || !window.FitAddon) return;
  var nonce = box.dataset.nonce;
  // xterm.js adds its own <style> elements: give them this page's nonce (Content-Security-Policy style-src)
  var make = document.createElement.bind(document);
  document.createElement = function (tag, opt) {
    var el = make(tag, opt);
    if (nonce && String(tag).toLowerCase() === 'style') el.nonce = nonce;
    return el;
  };
  var term = new window.Terminal({
    cursorBlink: true, scrollback: 5000, fontSize: 14,
    fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace',
    theme: { background: '#0b1416', foreground: '#d8e6e6', cursor: '#2ee6c5' }
  });
  var fit = new window.FitAddon.FitAddon();
  term.loadAddon(fit);
  term.open(box);
  try { fit.fit(); } catch (e) { /* hidden box */ }
  var state = document.getElementById('term-state');
  var again = document.getElementById('term-again');
  var enc = new TextEncoder();
  var ws = null;
  function show(on) {
    state.textContent = on ? state.dataset.on : state.dataset.off;
    state.className = 'pill ' + (on ? 's-ok' : 's-bad');
    again.hidden = on;
  }
  function connect() {
    var url = (location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + box.dataset.socket;
    ws = new WebSocket(url);
    ws.binaryType = 'arraybuffer';
    ws.onopen = function () {
      ws.send(JSON.stringify({ csrf: box.dataset.csrf, cols: term.cols, rows: term.rows }));
      show(true);
      term.focus();
    };
    ws.onmessage = function (e) {
      if (e.data instanceof ArrayBuffer) term.write(new Uint8Array(e.data));
    };
    ws.onclose = function (e) {
      show(false);
      term.write('\r\n\x1b[2m' + box.dataset.ended + (e.reason ? ' (' + e.reason + ')' : '') + '\x1b[0m\r\n');
      ws = null;
    };
  }
  term.onData(function (d) { if (ws && ws.readyState === 1) ws.send(enc.encode(d)); });
  term.onResize(function (s) { if (ws && ws.readyState === 1) ws.send(JSON.stringify({ cols: s.cols, rows: s.rows })); });
  if (window.ResizeObserver) new ResizeObserver(function () { try { fit.fit(); } catch (e) { /* hidden */ } }).observe(box);
  again.addEventListener('click', function () { term.reset(); connect(); });
  connect();
})();
