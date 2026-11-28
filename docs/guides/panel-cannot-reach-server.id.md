---
id: NP-TS-011
tier: user
keywords: agent, agen, not reachable, tidak terjangkau, tidak terhubung, status unknown, tidak diketahui, website tidak tampil
errors: noagent
---

# Panel tidak bisa menjangkau agennya

## Gejala
Website tidak tampil, statusnya "tidak diketahui", tombol menjawab "agent not reachable", atau form website baru
menulis bahwa agen tidak terjangkau.

## Penyebab
Panel hanya menampilkan dan meminta; layanan di server bernama **agen** yang menjalankan setiap perubahan website.
Saat agen berhenti, sedang restart atau terlalu sibuk, panel tidak bisa menjangkaunya. Websitenya sendiri tetap
berjalan: hanya panel yang sejenak tidak bisa melihat atau mengubahnya.

## Cara cek
1. Tunggu setengah menit lalu muat ulang halaman: setelah pembaruan atau restart, agen kembali dalam beberapa detik.
2. Admin: buka **Pengaturan → Sistem**. Agen tercantum di layanan server beserta keadaannya.

## Solusi
- Admin: tekan **Restart** di sebelah agen di halaman Sistem.
- Akun hosting: beri tahu admin server; tidak ada yang perlu diubah di website Anda.

## Masih belum bisa?
Kirim ke support kode artikel **NP-TS-011**, kode kejadian yang tampil di bawah galat, dan jamnya (dengan zona
waktunya).
