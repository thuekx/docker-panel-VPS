---
id: NP-MN-012
tier: user
keywords: ioncube, loader, php, encoded, lisensi, whmcs
---

# ionCube Loader

Beberapa aplikasi PHP (sistem billing, script berlisensi) dijual dalam bentuk ter-encode dengan ionCube dan menulis
"requires the ionCube Loader". Aktifkan loader itu hanya untuk website tersebut:

1. Buka website, lalu **Performa → ionCube Loader → Aktifkan ionCube**.
2. Server mengunduh loader dari ionCube sendiri, menambahkannya ke PHP website ini dan me-restart website (beberapa
   detik offline).
3. Panel memeriksa bahwa PHP mencantumkan loader itu. Kartu lalu menampilkan **Termuat** dengan versinya, misalnya
   `the ionCube PHP Loader v14.4.0`. Bila PHP tidak memuatnya, website kembali berjalan tanpa loader dan halaman
   menyebut alasannya.

Catatan:
- **Versi PHP**: saat Anda mengganti versi PHP, loader untuk versi baru ikut terpasang. Versi yang belum punya loader
  dari ionCube tidak ditawarkan selama ionCube menyala.
- **Unduhan**: server perlu menjangkau `downloads.ioncube.com` lewat HTTPS. Arsipnya disimpan 30 hari. Kartu
  menampilkan kapan diunduh, untuk versi PHP mana, dan sha256-nya.
- **Matikan ionCube**: menghapus loader. File ter-encode di website berhenti bekerja.
- **Di mana bisa dipakai**: hanya website Docker (WordPress, PHP biasa, Laravel). Mesin native memakai PHP bersama
  antar website, jadi tidak ditawarkan di sana.
- **Lisensi**: loader gratis dipakai dan kodenya tertutup. Milik ionCube Ltd. dan bukan bagian dari panel ini.
