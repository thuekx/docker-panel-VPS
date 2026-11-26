# Web terminal

Website → **Terminal** opens a shell inside that website's own container, in the browser. Use it for Composer,
WP-CLI, `php artisan`, `npm` or a quick look at the files.

1. The terminal is **off** for every website until you press **Turn the terminal on**. Everyone who can sign in to
   the panel can then open it.
2. The page shows where the shell runs: the container (for example `php`), the user (`33:33`, which the
   website's files belong to, never root) and the folder it starts in (`/var/www/html`, or `/app` for Node.js, Go and Python
   apps).
3. Type as in SSH. **Open again** reconnects after a session ends.

Limits and safety:
- The shell sees only that website's container: not the server, not other websites, not Docker.
- At most 2 sessions at once per website. A session ends after 15 minutes without typing, and after 2 hours in all.
- The audit log records who opened a session, when, for how long and how much went through it. What is typed or
  shown is not recorded.
- **Turn the terminal off** stops new sessions; an open one runs until it ends.
- Not available for native websites (use SFTP), static websites or ready apps (Uptime Kuma, n8n).
