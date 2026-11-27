# Redis per website

Website → **Performance** → **Redis** gives one website its own Redis. Its container, password and memory belong to
that website only, and other websites cannot reach it.

1. Choose a **Mode**:
   - **Cache (memory only)**: when memory is full the oldest keys go, and a restart empties it. Use this for an
     object cache.
   - **Store (kept on disk)**: nothing is thrown away (writes fail when full). Use this for sessions and queues.
2. Choose the **Memory** (64–512 MB). It counts toward the RAM of the website, plus 64 MB for Redis itself.
3. Press **Turn Redis on**. The website restarts with Redis (a few seconds). If it does not start, it runs as
   before and the page shows why.

How each kind of website uses it:
- **WordPress**: the panel installs and turns on the *Redis Object Cache* plugin. You do not need to do anything
  else.
- **Laravel**: install `predis/predis` with composer, then set `CACHE_STORE=redis`, `SESSION_DRIVER=redis` and/or
  `QUEUE_CONNECTION=redis` in the environment of the app. The host, port and password are already set.
- **Plain PHP**: read `REDIS_HOST`, `REDIS_PORT` and the password from the file named in `REDIS_PASSWORD_FILE`.
- **Node.js, Go, Python**: `REDIS_URL` (and `REDIS_HOST`, `REDIS_PORT`, `REDIS_PASSWORD`) are in the environment
  of the app.

The card shows the memory used, the number of keys and the hit rate. **Empty Redis** deletes every key. A cache
fills again by itself, but sessions and queues are lost. **Turn Redis off** removes Redis and its data. For
WordPress, the plugin is turned off first.

Redis data is not part of the backups.
