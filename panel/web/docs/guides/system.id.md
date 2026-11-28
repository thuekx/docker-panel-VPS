---
id: NP-ADM-009
tier: admin
keywords: sistem, layanan, service, proses, restart, docker, agen, file dikelola
---

# Halaman Sistem

**Pengaturan → Sistem** menampilkan apa yang menjalankan Nostek Panel sendiri.

## Layanan inti
- **Kontainer**: edge (HTTPS untuk setiap website), firewall aplikasi web, server SFTP, proxy socket Docker
  hanya-baca dan panel.
- **Layanan server**: Docker, agen Nostek dan fail2ban, ditambah Nginx dan MariaDB bila website native menyala.

Masing-masing menampilkan apakah berjalan baik. **Restart** me-restart satu layanan; website tetap berjalan.
- Restart panel memuat ulang halaman setelah beberapa detik.
- Agen me-restart dirinya beberapa detik setelah Anda menekannya.
- Docker tidak di-restart dari sini, karena itu menghentikan semua website. Pakai SSH untuk itu:
  `sudo systemctl restart docker`.

## Proses
15 proses tersibuk menurut CPU dan memori, diukur selama satu detik. **Ukur lagi** mengambil sampel baru. Setiap
proses menampilkan tempat ia berjalan: kontainer website (misalnya `np-site-voltix-id-php-1`) atau layanan server.
Hanya nama yang ditampilkan, tidak pernah perintah lengkapnya, karena perintah bisa memuat kata sandi.

## File yang dikelola
Untuk setiap website panel menulis `compose.yml`, `php.ini` dan pengaturan Nginx atau Apache dari yang Anda atur di
panel. Bagian ini membandingkannya dengan file di server.
- **cocok**: tidak ada yang perlu dilakukan.
- **berbeda**: buka file untuk melihat barisnya (`-` yang ada di server sekarang, `+` yang akan ditulis panel).
  **Tulis ulang** memasang versi panel dan me-restart website. Bila website tidak mau jalan dengannya, file lama
  kembali dengan sendirinya.

Editan tangan lewat SSH muncul di sini. Untuk menyimpan perubahan selamanya, buat di panel (pengaturan PHP, batas,
aturan keamanan), karena editan tangan hilang pada perubahan berikutnya dari panel. Website native tidak dibandingkan.
