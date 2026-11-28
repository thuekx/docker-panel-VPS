---
id: NP-ADM-006
tier: admin
keywords: api, kunci api, api key, token, ci, curl, otomatis, deploy, script
---

# API panel

Script dan pipeline CI memakai panel lewat API JSON kecil di `https://PANEL/api/v1`, dengan kunci API. API tidak
pernah menerima cookie login panel, dan panel tidak pernah menampilkan sebuah kunci untuk kedua kalinya.

## Kunci
Pengaturan → **API key** (`/settings/api`):
- **Nama**: untuk apa kunci ini (tampil di log aktivitas).
- **Hak**: setiap kunci bisa membaca; **Operasi** menambah mulai, henti dan restart; **Deploy** menambah deploy dari
  Git dan menerbitkan rilis statis.
- **Website**: semua website, atau satu saja. Kunci untuk satu website mendapat 404 untuk website lain, seolah-olah
  website itu tidak ada, dan hanya melihat websitenya sendiri.
- **Hanya dari alamat ini**: sampai 10 alamat atau jaringan (misalnya runner CI Anda); kosong = dari mana saja.
- **Berlaku selama** 30, 90 (bawaan) atau 365 hari, atau selamanya.
- Kata sandi akun Anda mengonfirmasi kunci baru. Kunci (`npk_…`, 47 karakter) tampil sekali saja; panel hanya
  menyimpan sha256-nya, beberapa karakter awal (untuk membedakan kunci), waktu dan alamat pemakaian terakhir. Paling
  banyak 20 kunci aktif.
- **Cabut** menghentikan kunci saat itu juga.

## Panggilan
Kirim `Authorization: Bearer npk_…`. Jawaban berupa JSON; galat berbentuk `{"error": "…"}`.

| Panggilan | Hak | Apa |
|---|---|---|
| `GET /api/v1/server` | baca | Versi, nama host, memori, disk, beban, kontainer, backup terakhir, hak kunci itu sendiri |
| `GET /api/v1/sites` | baca | Website: id, domain, nama, URL, aplikasi, mesin, status, kontainer jalan/total |
| `GET /api/v1/sites/{id}` | baca | Satu website; website statis juga menampilkan rilisnya |
| `POST /api/v1/sites/{id}/start` · `/stop` · `/restart` | operasi | Mulai, hentikan atau restart website |
| `POST /api/v1/sites/{id}/deploy` | deploy | Deploy website Node.js, Go atau Python dari branch Git-nya; menjawab `202 {"job": …}` |
| `PUT /api/v1/sites/{id}/release?name=dist.zip` | deploy | Terbitkan isi permintaan (sebuah .zip, sampai 512 MB) sebagai rilis baru website statis; menjawab `201 {"release": …}` |
| `GET /api/v1/jobs/{job}` | baca | Keadaan pekerjaan yang dimulai oleh pengguna kunci ini (`running`, `done`, `failed`) |

Kode status: 401 tanpa kunci atau kunci tidak sah (salah, dicabut, kedaluwarsa), 403 hak atau alamat yang tidak
dimiliki kunci, 404 website atau pekerjaan tidak ada (atau tidak boleh dilihat kunci itu), 429 terlalu banyak
panggilan (120 per menit per kunci; 10 panggilan per menit per alamat tanpa kunci yang sah).

## Contoh
```bash
KEY=npk_…   # from your CI secret store, never in the script itself
curl -fsS -H "Authorization: Bearer $KEY" https://panel.example.com/api/v1/sites

# publish a built static site (React, Vue, Astro …)
(cd dist && zip -qr ../dist.zip .)
curl -fsS -X PUT -H "Authorization: Bearer $KEY" --data-binary @dist.zip \
  "https://panel.example.com/api/v1/sites/my-site/release?name=dist.zip"

# deploy a Node.js website from Git and wait for the job
JOB=$(curl -fsS -X POST -H "Authorization: Bearer $KEY" https://panel.example.com/api/v1/sites/my-app/deploy | jq -r .job)
curl -fsS -H "Authorization: Bearer $KEY" https://panel.example.com/api/v1/jobs/$JOB
```

Setiap panggilan yang mengubah sesuatu tercatat di log aktivitas dengan nama kuncinya, tidak pernah kuncinya.
