# SFTP accounts (SFTP-01)

Status: [LIVE] v1.27.0, 2026-11-22 00:41 WIB (built 2026-11-21). Roadmap slot 12c, after SEC-01. Like cPanel's FTP Accounts, for one website
at a time, on a new **SFTP** tab. Port 2222 (decision SFTP-1).

## What a user gets
- Accounts per website, each with a name that is unique on the server (for example `budi.voltix`), a folder
  (`/` = the whole website, or a folder inside it such as `/wp-content/uploads`) and a password, public keys, or
  both. Up to 10 accounts per website and 10 keys per account.
- The page shows server, port and protocol and an `sftp://name@server:2222` line per account for FileZilla, WinSCP
  or Cyberduck. A password needs at least 12 characters; a key is one line from a `.pub` file (ed25519, ECDSA or
  RSA of 2048 bits or more; DSA is refused).
- A change of folder, password or keys ends the account's open connections; a removed account cannot log in again.
- Only file transfer: no shell, no command, no port forwarding, no links.

## How it runs
- A new core service `sftp` (container `nostek-core-sftp-1`) runs from the panel image with the program
  `/nostek-sftp`: an SSH server (golang.org/x/crypto/ssh) with the SFTP request server of github.com/pkg/sftp, both
  BSD-licensed Go modules. It publishes port 2222 and has its own network `nostek-sftp`, so it reaches no website
  container and not the panel.
- It mounts `<data>/sites` (read-write), `<data>/sftp` (the accounts, read-only) and `<data>/sftp-state` (the
  ed25519 host key, made on first start, 0600). Read-only root, every capability dropped except CHOWN,
  DAC_OVERRIDE and FOWNER (it runs as root inside only to give new files to the website's file user), no new
  privileges, 512 MB, 256 processes.
- Each session works through `os.Root` on the account's folder: no path, `..` or symbolic link reaches anything
  outside it. New files and folders belong to the website's file user (www-data for Docker websites, the
  website's own Linux user for native ones), modes keep only the permission bits, owners cannot be changed.
- Brute force: after 10 failed logins from one address within 10 minutes the address is refused for 30 minutes.
  At most 64 connections, 8 per address; 30 seconds to log in; 15 minutes without traffic closes a connection.
  Logins, failures and bans go to the container log (no password).

## Accounts
- The panel hashes a password with bcrypt; only the hash goes to the agent. The agent keeps a website's accounts
  only in `<data>/sftp/<id>.json` (0600, root): folder, file user and the accounts. site.json, answers and the
  audit log never hold a hash or a whole key (names, folders and key fingerprints only).
- The service reloads a file within 3 seconds when it changes; a broken file keeps the previous accounts; a name
  that two websites use works for neither (the agent refuses that name anyway).
- An account needs a password or a key at any time: the last one cannot be removed.
- Creating an account is refused until the panel update has started the service.
- Deleting a website moves its accounts into the trash entry; a restore brings them back (names another website
  took meanwhile are left out). At start the agent writes every file again with the current file user (a website
  moved to the native engine) and removes files of websites that are gone.

## Limits (known)
- The accounts live outside the website's backup, like the protected folders' passwords. Restoring a backup on
  the same server keeps them; on another server make them again.
- Port 2222 is opened by Docker; the panel update also adds a ufw rule so `ufw status` shows it.
- FTP and FTPS are not offered: SFTP only.

## Tests
- Service: unit tests with a real SSH client: upload, download, folder, rename (plain rename does not replace,
  POSIX rename does), chmod without setuid, truncate, times, listing; no way out through `..`, a link pointing
  outside or a new link; folder accounts; key and password logins; shell, command and port forwarding refused;
  wrong passwords, unknown names, a name used twice, a broken file; the ban and its end; a changed account ends
  its session while others keep theirs; new files owned by the file user.
- Agent: key parsing (types, RSA size, a type that does not match), every refusal, the file and its mode, the
  view without hash or key, the last way to log in kept, unique names across websites, the start sync, delete and
  restore.
- Panel: the page in both languages, every form and what reaches the agent (a hash, never the password), audit.
- Harness `security.sh`: the real `nostek-sftp` binary in a container like the core one, with OpenSSH's `sftp`
  and `ssh`: refused without the service, upload, folder and rename served by the website, the file user of new files,
  `..` and host files refused, no shell or port forward, another key refused, a folder account, a missing folder
  and a taken name refused, the agent's view, removal.
