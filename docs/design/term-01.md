# TERM-01: web terminal per website (v1.46.0)

Roadmap 15h (RAYX-2). A shell inside one website's own container, in the browser, off by default.

## Where the shell runs
- **In the website's container only**, never on the host and never as root:
  - **PHP websites** (WordPress, plain PHP, Laravel): the PHP service (`php`, `web` or `wordpress`, whichever
    mounts `php.ini`), as the file uid `33:33`, starting in `/var/www/html`.
  - **Node.js, Go, Python**: `app`, as the user of its compose `user:` line (refused when there is none, or when it
    is 0), starting in `/app`.
  - **Not offered**: native websites (their user lives on the host; they keep SFTP), static websites and ready apps.
- The jail is the container: its mounts, `no-new-privileges` and a non-root user. A shell cannot reach other
  websites, the host or the Docker socket.
- **Command**: `docker compose … exec -u <uid:gid> -e TERM=xterm-256color -e HOME=/tmp <service> sh -c '<bash, else
  sh>'`, run by the agent on a pseudo-terminal (`github.com/creack/pty`).

## Off by default
- `Site.Terminal` is set from the Terminal tab (`POST /v1/sites/{id}/terminal {on}`), and the change is audited.
- Turning it off refuses new sessions. Open sessions run on until they end.

## Session
- **Agent**: `GET /v1/sites/{id}/terminal/session?user=<panel user id>&cols=&rows=` on its Unix socket is upgraded
  to a WebSocket (`github.com/coder/websocket`).
  - Binary messages are terminal bytes both ways. A text message `{"cols","rows"}` resizes the terminal.
  - **Limits**: 2 sessions per website, 6 in all; 15 minutes without input or 2 hours in all ends a session.
  - On the end, SIGHUP, then kill after 5 s.
  - The server's read and write timeouts are lifted for the session only.
- **Panel**: `GET /terminal/{id}/socket` (signed in, with 2FA):
  - **Origin**: same origin only, through the WebSocket library's default check, so another site cannot use the
    panel's cookie.
  - **CSRF**: the first message carries the CSRF token and the size, so the token never sits in an address (access
    logs). A wrong or missing token closes with 1008 before the agent is asked.
  - **Proxy**: the panel then dials the agent and copies messages both ways. A refusal from the agent (off, too
    many sessions) reaches the browser as the close reason.
- **Audit**:
  - agent: `terminal` (open by user N in service as uid) and `terminal-end` (user, duration, why);
  - panel: `site.terminal-open` and `site.terminal-close` (duration, bytes in and out, who ended it).
  - What is typed or shown is never recorded.

## Browser
- **xterm.js 6.0.0** and **@xterm/addon-fit 0.11.0** are vendored unchanged in `panel/web/static/vendor/`: their
  npm integrity was checked, the sha256 is in `VERSIONS.sha256` and the licenses are in `LICENSE-xterm.md`.
  `script-src 'self'` is unchanged.
- **CSP**: xterm.js adds `<style>` elements, so the terminal page gets a style nonce (as the code editor does), and
  `terminal.js` gives that nonce to the style elements xterm creates.
- **Reconnect**: "Open again" after a session ends.
