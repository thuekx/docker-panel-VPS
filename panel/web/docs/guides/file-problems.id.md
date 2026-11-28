---
id: NP-TS-028
tier: user
keywords: file, upload, unggah, terlalu besar, too large, nama, sudah ada, exists, file manager
errors: file
---

# File tidak bisa diunggah, dibuat atau diganti nama

## Gejala
File Manager menjawab "Upload terlalu besar", "Nama tidak valid", "Belum ada file yang dipilih", "already exists" atau
"choose another name".

## Penyebab
- Satu unggahan lebih besar dari yang diterima panel.
- Nama memuat `/`, atau berupa `.` atau `..`.
- Sudah ada file atau folder dengan nama itu di folder tersebut.

## Cara cek
Lihat ukuran file dan nama-nama di folder tempat Anda berada.

## Solusi
- Terlalu besar: kemas file dalam .zip lalu unggah itu (File Manager bisa membongkarnya), atau unggah lewat SFTP.
- Nama: pilih nama tanpa `/`.
- Sudah ada: ganti nama atau hapus yang lama dulu, atau pilih nama lain.

## Masih belum bisa?
Kirim ke support kode artikel **NP-TS-028**, domain website, foldernya dan nama filenya.
