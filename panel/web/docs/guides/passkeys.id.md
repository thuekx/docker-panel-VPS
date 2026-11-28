---
id: NP-AL-002
tier: user
keywords: passkey, sidik jari, fingerprint, kunci keamanan, login, masuk, webauthn
---

# Masuk dengan passkey

Passkey membuat Anda bisa masuk ke panel dengan HP, kunci keamanan atau pembaca sidik jari komputer, tanpa mengetik
kata sandi dan kode 2FA.

## Tambah passkey
1. Buka panel dengan namanya (misalnya `https://panel.example.com`), bukan dengan alamat IP. Passkey terikat pada nama
   tempat ia dibuat.
2. Buka **Profil → Passkey**, beri nama (misalnya "HP"), ketik kata sandi akun lalu tekan **Tambah passkey**.
3. Browser meminta PIN, sidik jari atau wajah Anda. Passkey lalu muncul di daftar.

Anda bisa menambah sampai 10. Tambahkan passkey kedua di perangkat lain, supaya kehilangan satu perangkat tidak
mengunci Anda.

## Masuk
Di halaman masuk, tekan **Masuk dengan passkey** dan konfirmasi di perangkat Anda. Email atau kata sandi tidak
diperlukan.

## Hapus passkey
Profil → Passkey → **Hapus**, lalu ketik kata sandi akun. Hapus perangkat yang hilang saat itu juga.

## Bila tidak bekerja
- Tombolnya tidak muncul: browser tidak mendukung passkey, atau halaman tidak dibuka lewat HTTPS.
- Tidak ada passkey yang ditawarkan: panel dibuka dengan nama lain dari nama tempat passkey dibuat.
- Email, kata sandi dan kode 2FA selalu tetap bekerja, begitu juga kode pemulihan.
