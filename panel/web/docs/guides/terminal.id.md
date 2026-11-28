---
id: NP-ADM-010
tier: admin
keywords: terminal, shell, ssh, composer, wp-cli, artisan, npm
---

# Terminal web

Website → **Terminal** membuka shell di dalam kontainer website itu sendiri, di browser. Pakai untuk Composer, WP-CLI,
`php artisan`, `npm` atau melihat file sebentar.

1. Terminal **mati** untuk setiap website sampai Anda menekan **Aktifkan terminal**. Setelah itu semua admin yang bisa
   masuk ke panel bisa membukanya.
2. Halaman menampilkan tempat shell berjalan: kontainernya (misalnya `php`), usernya (`33:33`, pemilik file website,
   tidak pernah root) dan folder awalnya (`/var/www/html`, atau `/app` untuk aplikasi Node.js, Go dan Python).
3. Ketik seperti di SSH. **Buka lagi** menyambung kembali setelah sesi berakhir.

Batas dan keamanan:
- Shell hanya melihat kontainer website itu: bukan server, bukan website lain, bukan Docker.
- Paling banyak 2 sesi sekaligus per website. Sesi berakhir setelah 15 menit tanpa mengetik, dan setelah 2 jam total.
- Log aktivitas mencatat siapa membuka sesi, kapan, berapa lama dan berapa banyak data yang lewat. Apa yang diketik
  atau ditampilkan tidak dicatat.
- **Matikan terminal** menghentikan sesi baru; sesi yang terbuka berjalan sampai berakhir.
- Tidak tersedia untuk website native (pakai SFTP), website statis atau aplikasi siap pakai (Uptime Kuma, n8n).
