---
id: NP-TS-021
tier: user
keywords: dns, domain, record a, mengarah, point, resolve, preview, propagasi, nameserver
errors: dns
---

# Domain belum mengarah ke server ini

## Gejala
Menambah nama menjawab "does not resolve to a public internet address", halaman Domain menampilkan nama itu belum
mengarah ke sini, atau sertifikat sebuah nama terus menunggu.

## Penyebab
DNS nama itu (record A-nya) belum mengarah ke alamat publik server ini, atau perubahannya belum sampai ke semua server
DNS. Sertifikat hanya bisa terbit setelah nama mengarah ke sini.

## Cara cek
1. Di halaman **Domain** website, bandingkan alamat tujuan nama itu dengan alamat server ini yang tampil di sana.
2. Cek nama itu dengan pemeriksa DNS online. Perubahan bisa butuh beberapa menit sampai satu hari.

## Solusi
- Di penyedia DNS Anda, atur **record A** nama itu ke alamat server ini, dan hapus record **AAAA** yang mengarah ke
  tempat lain.
- Sambil menunggu, pakai alamat preview website.
- Di belakang proxy Cloudflare, pakai sertifikat sendiri (lihat [Sertifikat](certificates.md)).

## Masih belum bisa?
Kirim ke support kode artikel **NP-TS-021**, nama domainnya, dan di mana DNS-nya disimpan.
