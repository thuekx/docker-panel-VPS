---
id: NP-ADM-004
tier: admin
keywords: backup server, cadangan, pindah server, restore, pulihkan, s3, sftp, frasa sandi, passphrase
---

# Backup server dan pindah ke server baru

**Backup server** adalah satu file berisi seluruh panel dan setiap website, disegel dengan frasa sandi. Pakai untuk
memindahkan server ke VPS baru, atau menyimpan salinan lengkap sebelum perubahan besar. Dibuat di panel dan
dipulihkan oleh installer di server baru.

## Isinya

| Bagian | Apa yang kembali |
|---|---|
| Panel | Admin, login dua langkah dan kode pemulihan, pengaturan, zona waktu, peringatan (dengan rahasianya yang tersegel), kunci API, log aktivitas, riwayat login |
| Setiap website | File, database, pengaturannya (PHP, server, batas, environment, cron, jadwal), domain, pengaturan keamanan, kata sandi folder terlindung, akun SFTP, deploy key |

Tidak termasuk: backup lama setiap website, rilis statis lama, tempat sampah, pengaturan salinan malam di luar server
(backup restic milik server sendiri), dan sub-user API ClouDNS untuk sertifikat wildcard (isi lagi di server baru).
Website yang belum siap (masih dibuat, atau gagal) dilewati dan namanya dicatat di file.

## Membuatnya

1. **Pengaturan → Backup server**.
2. Ketik frasa sandi dua kali (paling sedikit 12 karakter) dan kata sandi akun, lalu **Buat backup server**.
3. Website tetap berjalan. Website besar butuh waktu; halaman menampilkan kemajuannya dan boleh ditinggal.
4. **Unduh** file saat sudah siap, atau salin dari `/srv/nostek/server-backups/` di server. 3 yang terbaru disimpan.

Frasa sandi tidak disimpan di mana pun. Tanpanya tidak ada yang bisa membuka file itu, termasuk panel, jadi simpan di
password manager. File dienkripsi dengan AES-256-GCM per bagian 64 KiB (kunci dari PBKDF2-SHA256, 600 000 putaran);
file yang diubah, dipotong atau diacak urutannya ditolak saat dibaca.

## Terjadwal, dengan salinan di luar server (v1.41.0)

**Pengaturan → Backup server → Backup server terjadwal**:
1. Pilih **Setiap hari** atau **Seminggu sekali**, jamnya (WIB) dan, untuk mingguan, harinya.
2. Ketik frasa sandi dua kali (paling sedikit 12 karakter) dan kata sandi akun, lalu **Simpan jadwal**.
   - Server hanya menyimpan **kunci publik** yang dibuat dari frasa sandi itu, tidak pernah frasa sandinya. Backup
     terjadwal disegel dengan kunci itu, jadi tidak butuh frasa sandi di malam hari, dan dibuka saat pemulihan dengan
     frasa sandi yang sama, dengan cara yang sama seperti di atas (`--restore`).
   - Mengubah jam atau hari kemudian tidak butuh frasa sandi. Frasa sandi baru hanya berlaku untuk backup berikutnya;
     file lama tetap dibuka dengan frasa sandi lama.
3. 3 yang terbaru tetap di server, sama seperti backup yang dibuat manual.

**Tujuan backup** (halaman yang sama) menerima salinan setelah setiap backup terjadwal:
- **Penyimpanan S3**: endpoint (`https://…`), bucket, folder (opsional), access key dan secret key. Layanan apa pun
  yang kompatibel S3 dengan alamat path-style bisa dipakai (IDCloudHost, Wasabi, Backblaze B2, MinIO, AWS). Beri kunci
  itu akses ke bucket tersebut saja.
- **Server SFTP**: host, port, user dan folder. Panel masuk dengan kunci SSH miliknya: tekan **Buat kunci SSH** dan
  tambahkan kunci publik yang ditampilkan ke `~/.ssh/authorized_keys` user itu. Host key server diingat saat uji
  pertama, dan host key yang berubah ditolak sesudahnya.
- Setiap tujuan menyimpan N salinan terbarunya (bawaan 7); yang lebih lama di sana dihapus. File lain dibiarkan.
- **Uji** menulis lalu menghapus satu file kecil. Setiap tujuan menampilkan salinan terakhir dan uji terakhirnya.
- **Salin ke …** di sebelah backup di daftar mengirim file itu secara manual.
- Kunci dan kata sandi disimpan di file khusus root di server dan tidak pernah ditampilkan lagi. Tidak ikut backup
  server: atur lagi tujuannya di server baru.

Bila backup terjadwal atau salah satu salinannya gagal, peringatan jenis **Backup** dikirim (Pengaturan →
Peringatan).

## Memulihkan di server baru

Server baru butuh yang sama dengan instalasi biasa (lihat [install.md](install.md)): Ubuntu 22.04/24.04, nama panel
mengarah ke server itu, port 80/443/2222 bebas, dan belum ada website.

Unduh dan periksa installer seperti di [install.md](install.md), lalu:

```bash
sudo bash nostek-install.sh INSTALL --domain panel.example.com --restore /root/nostek-server-20261126-170000.npbk
```

- Installer meminta frasa sandi (tidak terlihat). Untuk jalan tanpa terminal, taruh frasa sandi di file yang hanya
  bisa dibaca root lalu tambahkan `--passphrase-file /root/pass`; hapus file itu sesudahnya.
- Sebelum mengubah apa pun, installer memeriksa bahwa file itu backup server dan frasa sandinya cukup panjang. Setelah
  dasar server (pembaruan, Docker, firewall), installer memeriksa **seluruh file** (frasa sandi dan setiap bagian)
  sebelum panel dipasang. Frasa sandi salah berhenti di situ; jalankan perintah yang sama lagi dengan frasa yang
  benar.
- Lalu installer memasang panel dan agen, mengembalikan website satu per satu (pengaturan, lalu file dan database),
  dan memasang database panel. Tidak ada tautan admin yang dicetak: admin server lama masuk seperti biasa, dengan
  login dua langkahnya.
- Baris terakhir menampilkan setiap website: `restored`, `skipped` atau `failed`. Website yang gagal tidak
  menghentikan yang lain; backup-nya disimpan di `/srv/nostek/server-restore/<website>/backup.tar.gz`.

## Setelah pemulihan

1. Masuk dan periksa setiap website di alamat preview-nya.
2. Ubah DNS setiap website ke server baru. Sertifikat terbit begitu nama mengarah ke sana.
3. Siapkan lagi backup malam di luar server, dan buka TCP 2222 di penyedia bila SFTP dipakai.
4. Simpan server lama sampai website menjawab di server baru.

**Website native** (Nginx/PHP-FPM host) tidak dikembalikan otomatis: user Linux, pool PHP dan databasenya harus
dibuat dulu di server baru. Backup-nya disimpan di `/srv/nostek/server-restore/<website>/`. Buat website itu lagi
dengan domain yang sama dan mesin native, salin `backup.tar.gz` ke folder `backups/`-nya sebagai
`manual-YYYYMMDD-HHMMSS.tar.gz`, lalu pulihkan dari tab Backup-nya.
