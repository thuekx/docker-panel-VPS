---
id: NP-MN-006
tier: user
keywords: database, basis data, mariadb, mysql, postgresql, dbeaver, heidisql, repair, perbaiki, optimize, remote
---

# Database: akses dari luar, cek dan perbaiki

## Cek, perbaiki dan optimalkan tabel

Buka website, lalu **Database**. Di **Tabel**, centang tabel yang mau dikerjakan, atau jangan centang apa pun untuk
mengerjakan semuanya. Lalu tekan salah satu:

- **Periksa tabel** hanya membaca. Pakai saat halaman menampilkan galat database.
- **Perbaiki tabel** memperbaiki tabel yang dilaporkan rusak oleh cek. Bekerja pada tabel MyISAM dan Aria; tabel
  InnoDB menjawab bahwa tidak perlu diperbaiki.
- **Optimalkan** mengembalikan ruang kosong dan menyegarkan statistik.

Perbaiki dan optimalkan mengunci tabel sebentar, jadi buat backup dulu. Jawabannya menampilkan pesan database itu
sendiri untuk setiap tabel, dan log aktivitas mencatat jalannya.

PostgreSQL tidak punya cek atau perbaiki. **Optimalkan** di PostgreSQL menjalankan `VACUUM (ANALYZE)` pada seluruh
database.

## Akses dari luar

Ini membuat alat di komputer Anda (DBeaver, HeidiSQL, TablePlus, `mariadb`, `psql`) bisa membuka database website.
Bawaannya mati.

1. Di halaman **Database** website, pada **Akses database dari luar**, tulis alamat IP yang boleh terhubung: satu
   per baris, misalnya alamat kantor `203.0.113.7` atau jaringan `198.51.100.0/24`. Paling banyak 20, dan semua
   alamat (`0.0.0.0/0`) ditolak.
2. Tekan **Aktifkan akses dari luar**. Kartu lalu menampilkan server, port (MariaDB mulai 33061, PostgreSQL mulai
   54321), user dan perintah siap pakai.
3. Buka port TCP itu di firewall atau security group penyedia VPS, hanya untuk alamat yang sama.

Hanya alamat yang terdaftar yang bisa masuk. Alamat lain ditutup sebelum apa pun dibaca. Hanya koneksi terenkripsi
yang diloloskan:

- Klien MariaDB 11.4 dan lebih baru memakai TLS dengan sendirinya.
- Di alat lain, nyalakan SSL/TLS. Sertifikatnya dibuat server, jadi jangan minta verifikasi CA.
- PostgreSQL butuh `sslmode=require`.

Akunnya adalah user dan kata sandi database website itu sendiri (di `.env` atau `wp-config.php`), tidak pernah root.

**Simpan daftar** mengubah alamat dan mempertahankan port. **Matikan akses dari luar** menutup port. Memindahkan
website ke tempat sampah juga menutup port, dan memulihkannya membuka port lagi.

Website native (MariaDB host) tidak ditawari akses dari luar, karena server databasenya dipakai bersama.
