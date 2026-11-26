# ionCube Loader

Some PHP applications (billing systems, licensed scripts) are sold encoded with ionCube and say "requires the ionCube
Loader". Turn the loader on for that website only:

1. Open the website, then **Performance → ionCube Loader → Turn ionCube on**.
2. The server downloads the loader from ionCube itself, adds it to this website's PHP and restarts the website (a few
   seconds offline).
3. The panel checks that PHP lists the loader. The card then shows **Loaded** with its version, for example
   `the ionCube PHP Loader v14.4.0`. If PHP does not load it, the website runs without it again and the page says
   why.

Notes:
- **PHP version**: when you change the PHP version, the loader of the new version comes along. Versions ionCube has
  no loader for yet are not offered while ionCube is on.
- **Download**: the server needs to reach `downloads.ioncube.com` over HTTPS. The archive is kept for 30 days. The
  card shows when it was downloaded, for which PHP versions, and its sha256.
- **Turn ionCube off**: removes the loader. Encoded files of the website stop working.
- **Where it works**: Docker websites only (WordPress, plain PHP, Laravel). The native engine shares PHP between
  websites, so it is not offered there.
- **License**: the loader is free to use and closed source. It belongs to ionCube Ltd. and is not part of this
  panel.
