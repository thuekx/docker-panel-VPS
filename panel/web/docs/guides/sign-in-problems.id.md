---
id: NP-TS-001
tier: user
open: yes
keywords: login, masuk, sign in, password, kata sandi, 2fa, kode, authenticator, terkunci, locked, tautan, link, kode pemulihan, recovery
errors: login, linkgone
---

# Tidak bisa masuk

## Gejala
Halaman masuk menulis "E-mail atau kata sandi salah", "Kode tidak valid", "Terlalu banyak percobaan", atau tautan setup
menulis "Link tidak berlaku".

## Penyebab
- Email atau kata sandi salah ketik, atau Caps Lock menyala.
- Kode 2FA sudah lama, atau jam di HP tidak tepat (kode berganti setiap 30 detik).
- Setelah banyak percobaan salah, panel menunggu satu menit sebelum menerima percobaan berikutnya.
- Tautan setup atau tautan kata sandi sekali pakai hanya bekerja sekali dan untuk waktu singkat.

## Cara cek
1. Ketik kata sandi di editor teks dulu supaya terlihat, lalu tempel.
2. Di HP, nyalakan tanggal dan jam otomatis, lalu pakai kode terbaru.

## Solusi
- Kata sandi salah: admin server ini bisa membuat tautan setup baru untuk Anda.
- HP hilang: di halaman 2FA pakai salah satu **kode pemulihan** sebagai pengganti kode. Masing-masing berlaku sekali.
- Terlalu banyak percobaan: tunggu satu menit, lalu coba sekali dengan hati-hati.
- Link tidak berlaku: minta tautan baru ke orang yang mengirimnya.
- Passkey: lihat [Masuk dengan passkey](passkeys.md).

## Masih belum bisa?
Kirim ke support kode artikel **NP-TS-001**, email Anda di panel, dan jam Anda mencoba (dengan zona waktunya,
misalnya 19:40 WIB). Jangan pernah mengirim kata sandi atau kode.
