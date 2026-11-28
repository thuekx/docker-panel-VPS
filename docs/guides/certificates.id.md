---
id: NP-MN-009
tier: user
keywords: sertifikat, certificate, https, ssl, tls, lets encrypt, csr, wildcard, cloudns, cloudflare, self-signed
errors: cloudns
---

# Sertifikat: otomatis, milik sendiri dan wildcard

Setiap nama website mendapat sertifikat **Let's Encrypt** gratis dengan sendirinya, begitu DNS-nya mengarah ke server
ini. Untuk itu tidak perlu melakukan apa pun. Panduan ini untuk keadaan lainnya.

## Sertifikat sendiri (satu website)

Pakai untuk sertifikat origin Cloudflare di belakang proxy Cloudflare ("Full (strict)"), sertifikat yang dibeli di
tempat lain, atau dari CA perusahaan.

1. Buka website, lalu **Domain → Sertifikat → Pakai sertifikat sendiri**.
2. Tempel sertifikat dalam format PEM (sertifikat website dulu, lalu rantainya) dan kunci privatnya dalam PEM.
3. **Simpan sertifikat**. Agen memeriksa bahwa kunci cocok dengan sertifikat, sertifikat berlaku sekarang dan paling
   tidak satu hari lagi, dan mencakup nama utama website. Edge langsung memakainya.

Nama website yang tidak dicakup sertifikat itu tetap memakai sertifikat Let's Encrypt. Sertifikat yang tidak
dipercaya CA publik (sertifikat origin Cloudflare salah satunya) diterima dengan peringatan: browser yang langsung
datang ke server tidak memercayainya. **Hapus sertifikat sendiri** kembali ke Let's Encrypt.

Kunci hanya disimpan di server (`tls/key.pem` di folder website dan satu salinan untuk edge, keduanya hanya bisa
dibaca root dan edge). Kunci tidak pernah ditampilkan lagi dan tidak ada di log mana pun. Kunci ikut pindah bersama
backup website, tempat sampah dan backup server. Peringatan jenis **Sertifikat sendiri hampir habis** datang 14 hari
sebelum berakhir (Pengaturan → Peringatan).

## Permintaan sertifikat (CSR): sertifikat yang Anda beli

Pakai ini bila otoritas sertifikat (Sectigo, DigiCert, GlobalSign, reseller) meminta CSR. Kunci privat dibuat di
server ini dan tidak pernah keluar.

1. **Domain → Sertifikat → Buat permintaan sertifikat (CSR)**.
2. Centang nama-namanya. Nama utama harus termasuk, dan `*.domain` tersedia untuk wildcard. Pilih kunci: ECDSA P-256
   disarankan, RSA tersedia untuk otoritas yang memintanya.
3. Sertifikat tervalidasi domain hanya butuh nama. Untuk sertifikat tervalidasi organisasi, isi organisasi, kota,
   provinsi dan negara persis seperti terdaftar.
4. **Buat permintaan**. Salin permintaan, dari `-----BEGIN CERTIFICATE REQUEST-----` sampai baris END, ke formulir
   pemesanan otoritas.
5. Saat sertifikat datang, buka **Pakai sertifikat sendiri**, tempel sertifikat dengan rantainya, dan **biarkan kunci
   privat kosong**. Agen memasangkannya dengan kunci yang dibuatnya. Permintaan lalu selesai dan dihapus.

Permintaan baru menggantikan yang sedang menunggu, dan kuncinya dihapus. **Hapus permintaan** membuang permintaan
dan kuncinya. Kunci yang menunggu disimpan di `tls/request/` di folder website (hanya bisa dibaca root) dan ikut
pindah bersama backup.

## Sertifikat self-signed: nama uji dan jaringan privat

**Sertifikat self-signed** membuat sertifikat yang ditandatangani kuncinya sendiri, untuk nama yang dicentang,
berlaku 30 sampai 825 hari. Edge langsung memakainya. Browser menampilkan peringatan untuknya, jadi pakai hanya
untuk nama uji, jaringan privat, atau di belakang proxy yang tidak memeriksa sertifikat. Ganti dengan **Pakai
sertifikat sendiri**, atau kembali ke Let's Encrypt dengan **Hapus sertifikat sendiri**.

## Sertifikat wildcard lewat ClouDNS

Wildcard (`*.example.com`) adalah satu sertifikat untuk sebuah zona dan semua nama satu tingkat di bawahnya. Let's
Encrypt memberi wildcard hanya lewat pemeriksaan DNS: edge menambah record TXT `_acme-challenge` sebentar di zona lalu
menghapusnya lagi. Untuk itu dibutuhkan sub-user API ClouDNS, tempat zona disimpan. Paket ClouDNS gratis mungkin
tidak punya akses API; periksa di **API & Resellers** pada akun ClouDNS Anda.

### Siapkan sub-user sekali saja

1. Di ClouDNS buka **API & Resellers → API sub-users** dan tambahkan sub-user. Batasi ke zona yang butuh wildcard.
   Bila ClouDNS meminta alamat IP yang diizinkan, tambahkan alamat publik server ini.
2. Di panel buka **Pengaturan → Sertifikat wildcard (ClouDNS)**, tempel sub-auth-id dan kata sandinya, lalu **Uji
   dan simpan**. Panel lebih dulu meminta ClouDNS masuk dengan data itu; bila ditolak, panel menampilkan kata-kata
   ClouDNS sendiri.
3. Edge restart beberapa detik untuk membaca sub-user baru: semua website dan panel mungkin tidak terbuka sejenak.

Kata sandi hanya disimpan di `/srv/nostek/edge/dns/` (bisa dibaca root dan edge), tidak pernah di database panel,
halaman, file compose atau log. Kata sandi ini tidak ikut backup server: isi lagi di server baru.

### Pakai untuk sebuah website

Di halaman **Domain** website, pada **Sertifikat wildcard**, pilih zona (nama utama atau zona di atasnya) lalu
**Pakai wildcard**. Agen menanyakan ke ClouDNS apakah sub-user bisa melihat zona itu, lalu memasang nama-nama website
di zona itu ke wildcard. Nama lainnya (misalnya alamat preview, atau nama dua tingkat di bawah) tetap memakai Let's
Encrypt lewat HTTP. Wildcard pertama butuh sekitar satu menit; halaman Domain menampilkan sertifikat begitu sudah
ada. **Matikan wildcard** kembali ke satu sertifikat per nama.

Sub-user tidak bisa dihapus selama ada website yang memakai wildcard; halaman pengaturan menyebut website itu.
Website yang masih di alamat preview belum bisa memakai wildcard sampai domainnya sendiri menjadi alamat utama.
