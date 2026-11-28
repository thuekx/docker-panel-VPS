---
id: NP-MN-006
tier: user
keywords: database, basis data, mariadb, mysql, postgresql, dbeaver, heidisql, repair, perbaiki, optimize, remote
---

# Database: access from outside, check and repair

## Check, repair and optimize tables

Open the website, then **Database**. Under **Tables**, tick the tables to work on, or tick none to work on all of
them. Then press one of:

- **Check tables** only reads. Use it when a page shows database errors.
- **Repair tables** fixes a table that check reports as broken. It works on MyISAM and Aria tables; InnoDB tables
  answer that they need no repair.
- **Optimize** gives free space back and refreshes the statistics.

Repair and optimize lock a table for a moment, so make a backup first. The answer shows the database's own message
for each table, and the audit log records the run.

PostgreSQL has no check or repair. Its **Optimize** runs `VACUUM (ANALYZE)` on the whole database.

## Access from outside

This lets a tool on your computer (DBeaver, HeidiSQL, TablePlus, `mariadb`, `psql`) open a website's database. It
is off by default.

1. On the website's **Database** page, under **Database access from outside**, list the IP addresses that may
   connect: one per line, for example your office address `203.0.113.7` or a network `198.51.100.0/24`. At most 20
   are allowed, and everyone (`0.0.0.0/0`) is refused.
2. Press **Turn access from outside on**. The card then shows the server, the port (MariaDB from 33061, PostgreSQL
   from 54321), the user and a ready command.
3. Open that TCP port in the VPS provider's firewall or security group, for the same addresses only.

Only listed addresses get in. Every other address is closed before anything is read. Only encrypted connections
are let through:

- MariaDB 11.4 and newer clients use TLS by themselves.
- In other tools, turn SSL/TLS on. The certificate is made by the server, so do not ask for CA verification.
- PostgreSQL needs `sslmode=require`.

The account is the website's own database user and password (in its `.env` or `wp-config.php`), never root.

**Save list** changes the addresses and keeps the port. **Turn access from outside off** closes the port. Moving
the website to the trash closes the port too, and a restore opens it again.

Native websites (host MariaDB) are not offered access from outside, because their database server is shared.
