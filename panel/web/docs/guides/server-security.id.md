---
id: NP-ADM-007
tier: admin
keywords: firewall, ufw, port, fail2ban, blokir, diblok, ip, unblock
---

# Keamanan server: firewall dan alamat yang diblokir

**Pengaturan → Keamanan server** menampilkan firewall server dan alamat yang diblokir fail2ban.

## Buka atau tutup port
1. Di **Firewall**, ketik port (misalnya `8080`, atau rentang seperti `8000:8100`), pilih protokol, dan bila perlu
   alamat atau jaringan yang boleh (kosong = dari mana saja).
2. Pilih **Izinkan** atau **Tolak**, ketik kata sandi akun lalu tekan **Tambah aturan (dicoba)**.
3. Perubahan langsung berlaku, tapi sebagai **uji coba 10 menit**. Untuk mempertahankannya, buka panel di **jendela
   privat baru** (atau browser lain), masuk, buka Pengaturan → Keamanan server lalu tekan **Pertahankan perubahan**.
   Bila Anda tidak melakukan apa pun, firewall kembali seperti semula dengan sendirinya.

Menghapus aturan bekerja dengan cara yang sama (**Hapus**, kata sandi, lalu konfirmasi dari jendela baru).

Port SSH tidak pernah ditutup dari panel, dan aturan untuk website (80, 443) serta akses database dari luar tetap
dijaga. Port 80, 443 dan 2222 dibuka oleh Docker dan melewati firewall.

## Alamat yang diblokir
fail2ban memblokir sebuah alamat selama satu jam setelah terlalu banyak gagal masuk:
- **SSH**: jail milik server sendiri.
- **Masuk panel** dan **SFTP**: 10 kegagalan dalam 10 menit. Alamat itu lalu diblokir dari semua yang berjalan di
  Docker di server ini, termasuk website, selama satu jam itu.

Tekan **Buka blokir** di sebelah alamat untuk mencabut blokir saat itu juga.

## Jangan pernah blokir
Tulis alamat atau jaringan tempat Anda masuk di **Jangan pernah blokir** (satu per baris), supaya salah ketik tidak
pernah mengunci Anda. Halaman menampilkan alamat Anda saat ini.
