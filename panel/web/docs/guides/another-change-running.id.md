---
id: NP-TS-010
tier: user
keywords: sibuk, busy, berjalan, sedang berjalan, tunggu, menunggu, tombol mati, disabled, another change, perubahan lain
errors: busy
---

# Tombol abu-abu: perubahan lain sedang berjalan

## Gejala
Tombol-tombol sebuah website berwarna abu-abu, halaman menyebut website sedang sibuk, atau sebuah tindakan menjawab
"another change is running; try again when it is done".

## Penyebab
Satu website hanya menerima satu perubahan sekali jalan, supaya dua perubahan tidak saling menimpa. Selama pembuatan,
deploy, backup, pemulihan, migrasi, pembaruan atau pembaruan image berjalan, perubahan lain di website itu menunggu.

## Cara cek
1. Buka ringkasan website: pekerjaan yang berjalan dan langkah-langkahnya tampil di atas.
2. Halaman memperbarui dirinya sendiri. Sebagian besar perubahan hanya beberapa detik; backup atau pemulihan website
   besar bisa beberapa menit.

## Solusi
Tunggu sampai pekerjaan itu selesai atau gagal, lalu tekan tombolnya lagi. Tidak ada yang perlu dibatalkan:
perubahan yang Anda coba belum dimulai.

## Masih belum bisa?
Bila pekerjaan yang sama berjalan lebih dari satu jam, kirim ke support kode artikel **NP-TS-010**, domain website,
nama pekerjaan dan kapan mulainya (dengan zona waktunya).
