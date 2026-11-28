---
id: NP-MN-021
tier: user
keywords: redis, cache, object cache, sesi, session, queue, antrean
---

# Redis per website

Website → **Performa** → **Redis** memberi satu website Redis miliknya sendiri. Kontainer, kata sandi dan memorinya
hanya milik website itu, dan website lain tidak bisa menjangkaunya.

1. Pilih **Mode**:
   - **Cache (hanya memori)**: saat memori penuh kunci tertua dibuang, dan restart mengosongkannya. Pakai untuk
     object cache.
   - **Store (disimpan di disk)**: tidak ada yang dibuang (penulisan gagal saat penuh). Pakai untuk sesi dan antrean.
2. Pilih **Memori** (64–512 MB). Dihitung ke RAM website, ditambah 64 MB untuk Redis sendiri.
3. Tekan **Aktifkan Redis**. Website restart dengan Redis (beberapa detik). Bila tidak mau jalan, website berjalan
   seperti sebelumnya dan halaman menyebut alasannya.

Cara setiap jenis website memakainya:
- **WordPress**: panel memasang dan menyalakan plugin *Redis Object Cache*. Tidak ada lagi yang perlu dilakukan.
- **Laravel**: pasang `predis/predis` dengan composer, lalu atur `CACHE_STORE=redis`, `SESSION_DRIVER=redis` dan/atau
  `QUEUE_CONNECTION=redis` di environment aplikasi. Host, port dan kata sandi sudah diatur.
- **PHP biasa**: baca `REDIS_HOST`, `REDIS_PORT` dan kata sandi dari file yang disebut di `REDIS_PASSWORD_FILE`.
- **Node.js, Go, Python**: `REDIS_URL` (dan `REDIS_HOST`, `REDIS_PORT`, `REDIS_PASSWORD`) ada di environment
  aplikasi.

Kartu menampilkan memori terpakai, jumlah kunci dan hit rate. **Kosongkan Redis** menghapus semua kunci. Cache akan
terisi lagi dengan sendirinya, tapi sesi dan antrean hilang. **Matikan Redis** menghapus Redis dan datanya. Untuk
WordPress, pluginnya dimatikan lebih dulu.

Data Redis tidak ikut backup.
