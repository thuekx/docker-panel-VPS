---
id: NP-ADM-003
tier: admin
keywords: update, pembaruan, versi, upgrade, rilis, rollback
---

# Memperbarui Nostek Panel dari panel

Mulai v1.38.0, rilis baru dipasang dari **Pengaturan → Pembaruan**. Agen dan panel pindah ke rilis itu bersama;
website tetap berjalan.

## Perbarui
1. Buka **Pengaturan → Pembaruan**. Halaman menampilkan rilis yang lebih baru dengan catatannya. **Periksa lagi**
   langsung bertanya ke GitHub (bila tidak, daftarnya disimpan 10 menit).
2. Pilih rilis (yang terbaru sudah terpilih).
3. Biarkan **Buat backup server lebih dulu** tercentang dan ketik frasa sandi dua kali (paling sedikit 12 karakter, tidak
   disimpan di mana pun: simpan di password manager Anda). Backup-nya masuk ke Pengaturan → Backup server seperti
   backup lainnya.
4. Ketik kata sandi akun lalu tekan **Perbarui sekarang**.
5. Halaman menampilkan langkah-langkah saat berjalan. Menjelang akhir panel restart sebentar; halaman terus bertanya
   dan menampilkan hasilnya sendiri.

## Bila ada yang gagal
Setiap langkah diperiksa. Bila agen atau panel baru tidak mau jalan, atau pemeriksaan (VERIFY) gagal sesudahnya,
rilis sebelumnya dipasang kembali bersama database panel dari sesaat sebelum pembaruan, dan halaman menampilkan
**Dikembalikan** dengan alasannya. Keluaran lengkapnya ada di server di `/var/log/nostek-update.log`.

Bila halaman menampilkan **Gagal**, pemasangan kembali rilis sebelumnya juga gagal. Panel mungkin tidak terbuka; di
server, `sudo cat /var/log/nostek-update.log` menunjukkan apa yang terjadi, dan backup server yang dibuat sebelum
pembaruan bisa dipulihkan.

## "Belum bisa di server ini"
Server menjalankan rilis dari sebelum v1.38.0. Pasang v1.38.0 sekali dengan runbook pembaruan; setiap rilis
sesudahnya bisa datang dari panel.
