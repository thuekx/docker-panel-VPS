---
id: NP-ADM-001
tier: admin
keywords: install, instal, pasang, server baru, vps, ubuntu, debian, arm64, installer
---

# Memasang Nostek Panel di server baru

Satu perintah memasang Nostek Panel di server Ubuntu atau Debian yang baru: dasarnya (zona waktu, pembaruan, swap,
Docker Engine, fail2ban, firewall), panel dan agennya. Di akhir, installer mencetak tautan sekali pakai untuk membuat
admin pertama.

## Sebelum mulai

- Server **Ubuntu 22.04 atau 24.04** atau **Debian 12** yang baru (amd64 atau arm64), memori minimal **1 GB** (2 GB
  atau lebih lebih baik) dan **disk kosong 15 GB**, dengan akses root atau sudo lewat SSH. Di Debian 12 dan arm64
  installer memakai v1.47.0 atau lebih baru. Website berjalan di mesin Docker di mana saja; mesin native (Nginx +
  PHP-FPM host) butuh Ubuntu 24.04. Debian minimal mungkin tidak punya curl: jalankan `apt-get install -y curl` dulu.
- Nama untuk panel, misalnya `panel.example.com`, dengan **record A** ke alamat IPv4 publik server. Sertifikat HTTPS
  membutuhkannya.
- Tidak wajib tapi berguna: record wildcard `*.preview.example.com` ke alamat yang sama. Website yang domainnya belum
  mengarah ke sini mendapat alamat preview di bawahnya. Bawaannya zona preview = `preview.` + nama panel tanpa bagian
  pertamanya; ubah dengan `--preview-zone`.
- Port **80** dan **443** terbuka di penyedia (firewall atau security group-nya), dan **2222** bila ingin SFTP. Tidak
  boleh ada yang lain memakai port 80, 443 atau 2222 di server.

## Pasang

```bash
curl -fsSLO https://raw.githubusercontent.com/thuekx/docker-panel-VPS/v1.47.0/packages/inst-01/nostek-install.sh
echo "ef9c51dcf31a6d1d8a9d3ead60783231aaec97c3f4b549be52f555b719a16f36  nostek-install.sh" | sha256sum -c -
sudo bash nostek-install.sh PREFLIGHT --domain panel.example.com
sudo bash nostek-install.sh INSTALL --domain panel.example.com --timezone Asia/Jakarta
```

- `PREFLIGHT` hanya memeriksa dan tidak mengubah apa pun: versi sistem dan arsitektur, memori, disk, nama host, alamat
  publik, DNS nama panel, wildcard preview, port yang bebas dan sumber unduhan (Docker, GitHub, Docker Hub).
- `INSTALL` menjalankan pemeriksaan yang sama, menampilkan apa yang akan dikerjakan dan meminta Anda mengetik `yes`
  (atau tambahkan `--yes`). Biasanya 10 sampai 20 menit, sebagian besar untuk membangun panel.
- Keluarannya juga disimpan di `/var/log/nostek-install.log` (hanya bisa dibaca root).

Opsi: `--preview-zone ZONA`, `--timezone Wilayah/Kota` (bawaan: pakai zona server), `--release vX.Y.Z` (bawaan: rilis
terbaru) atau `--src-commit SHA`, `--ip ALAMAT` (bila alamat publik tidak bisa ditemukan sendiri), `--yes`.

## Apa yang diubah

| Bagian | Apa |
|---|---|
| Zona waktu | Diatur bila `--timezone` diberikan. |
| Paket | `apt upgrade`, pembaruan keamanan otomatis, fail2ban, ufw, git. |
| Swap | `/swapfile` 2 GB bila server belum punya swap. |
| Docker Engine | Dari repositori Docker sendiri; sidik jari kunci tanda tangannya diperiksa dulu. Batas log dan live-restore. |
| fail2ban | Menjaga setiap port tempat SSH mendengar. |
| Firewall (ufw) | Masuk ditutup kecuali SSH (setiap portnya), 80 dan 443. Sebuah timer mematikan firewall lagi setelah 10 menit bila instalasi berhenti sebelum aturannya diperiksa. Kontainer yang membuka port (edge panel di 80/443, SFTP di 2222) dibuka oleh Docker sendiri. |
| Panel | Stack inti di `/srv/nostek/core` dan agen `nostek-agent`, dibangun dari rilis. |

SSH sendiri tidak diubah: user, port dan kunci yang sama tetap bekerja. Untuk pindah ke user sendiri dan port lain,
lihat [mengganti user login bawaan](change-default-user.md).

## Cara rilis diperiksa

Installer mencari commit rilis dari tag-nya, mengambil tepat commit itu dengan git (id commit adalah hash isinya,
jadi git menolak yang lain) dan memeriksa script instalasi panel dan agen terhadap sha256 di file kunci rilis itu
sendiri sebelum menjalankannya. Image dasar dan layanan dikunci dengan digest di dalam rilis. Installer sendiri
diperiksa oleh baris `sha256sum -c` di atas.

## Setelah instalasi

1. Buka tautan di baris terakhir dalam 30 menit dan buat admin pertama.
2. **Backup belum disiapkan.** Dasbor menampilkan "Belum ada hasil backup" sampai backup malam berjalan. Siapkan
   backup di luar server sebelum menaruh website sungguhan di server.
3. Tautan admin baru di kemudian hari: installer mencetak perintah `SETUP-LINK` yang tepat di akhir.

Bila satu langkah gagal, keluarannya menyebut langkah mana; perbaiki penyebabnya dan jalankan `INSTALL` lagi.
Instalasi yang sudah selesai menolak dijalankan dua kali.

## Memindahkan server ke sini

`--restore FILE` memasang server ini dari backup server Nostek Panel lain (v1.34.0 atau lebih baru): panel, adminnya
dan setiap website kembali. Lihat [server-backup.md](server-backup.md).
