---
id: NP-ADM-002
tier: admin
keywords: ssh, user, port, ubuntu, root, login, sudo, keamanan
---

# Mengganti user bawaan VPS dan port SSH dengan aman

Untuk server Ubuntu 22.04 / 24.04 yang dibuat penyedia VPS dengan user bawaan (sering `ubuntu`, `root` atau
sejenisnya). Hasilnya: user Anda sendiri, port SSH pilihan Anda, dan user lama dikunci (tidak dihapus).

Isian yang harus diganti di semua tempat:

| Isian | Contoh | Arti |
|---|---|---|
| `NEWUSER` | `alice` | User login baru Anda |
| `NEWPORT` | `22022` | Port SSH baru (1024-65535, tidak dipakai yang lain) |
| `OLDUSER` | `ubuntu` | User yang dibuat penyedia |
| `SERVER` | alamat server Anda | IP atau nama host server |

> **Aturan emas:** biarkan sesi SSH Anda yang sekarang tetap terbuka sampai langkah 6 dikonfirmasi dari sesi *baru*.
> Port 22 tetap terbuka sampai port baru terbukti bekerja.

## Langkah 0 — sebelum mulai (konsol penyedia)

1. Buat **snapshot** VPS di konsol penyedia.
2. Bila penyedia punya firewall / security group sendiri, **izinkan TCP `NEWPORT`** di sana (biarkan 22 terbuka dulu).

## Langkah 1 — buat user baru (sebagai OLDUSER)

```bash
sudo adduser NEWUSER            # asks for the new password twice (hidden)
sudo usermod -aG sudo NEWUSER
```

Bila Anda masuk dengan kunci SSH, salin ke user baru:

```bash
sudo install -d -m 700 -o NEWUSER -g NEWUSER /home/NEWUSER/.ssh
sudo install -m 600 -o NEWUSER -g NEWUSER ~/.ssh/authorized_keys /home/NEWUSER/.ssh/authorized_keys
```

## Langkah 2 — buka port SSH baru di samping port 22

Ubuntu membaca `/etc/ssh/sshd_config.d/*.conf` menurut urutan nama dan nilai **pertama** yang menang, jadi pakai nama
yang urutannya sebelum file penyedia (misalnya `10-…`).

```bash
printf 'Port 22\nPort NEWPORT\n' | sudo tee /etc/ssh/sshd_config.d/10-custom-ssh.conf
# only if you want password login for NEWUSER:
echo 'PasswordAuthentication yes' | sudo tee -a /etc/ssh/sshd_config.d/10-custom-ssh.conf
sudo sshd -t && echo "SSHD_CONFIG_OK"
sudo ufw status | grep -q active && sudo ufw allow NEWPORT/tcp
```

## Langkah 3 — jaring pengaman, lalu terapkan

Jadwalkan pembatalan otomatis dalam 10 menit (dibatalkan di langkah 5 bila semua baik):

```bash
sudo systemd-run --unit=ssh-revert --on-active=10min \
  sh -c 'mv /etc/ssh/sshd_config.d/10-custom-ssh.conf /root/10-custom-ssh.conf.reverted; systemctl daemon-reload; systemctl restart ssh.socket 2>/dev/null; systemctl restart ssh'
```

Terapkan port baru:

```bash
# Ubuntu 24.04 (SSH started by systemd socket):
sudo systemctl daemon-reload && sudo systemctl restart ssh.socket
# Ubuntu 22.04:
sudo systemctl restart ssh
sudo ss -tlnp | grep -E ':(22|NEWPORT) '      # both ports must be listed
```

## Langkah 4 — uji dari terminal BARU (biarkan yang lama terbuka)

```bash
ssh -p NEWPORT NEWUSER@SERVER
sudo -v && echo "SUDO_OK"
```

## Langkah 5 — konfirmasi (di sesi baru)

```bash
sudo systemctl stop ssh-revert.timer && echo "REVERT_CANCELLED"
```

Bila langkah 4 gagal, jangan lakukan apa pun: setelah 10 menit server kembali ke pengaturan lama dengan sendirinya.

## Langkah 6 — tutup port 22

```bash
sudo sed -i '/^Port 22$/d' /etc/ssh/sshd_config.d/10-custom-ssh.conf
sudo sshd -t && echo "SSHD_CONFIG_OK"
sudo systemctl daemon-reload && (sudo systemctl restart ssh.socket 2>/dev/null; sudo systemctl restart ssh)
sudo ufw status | grep -q active && sudo ufw delete allow 22/tcp ; sudo ufw delete allow OpenSSH 2>/dev/null
```

Masuk sekali lagi dari terminal baru di `NEWPORT`, lalu hapus port 22 dari firewall penyedia.

## Langkah 7 — kunci user lama (jangan dihapus)

```bash
sudo passwd -l OLDUSER
sudo usermod -s /usr/sbin/nologin OLDUSER
sudo mv /home/OLDUSER/.ssh/authorized_keys /home/OLDUSER/.ssh/authorized_keys.disabled 2>/dev/null
```

## Disarankan sesudahnya

- Dengan login kata sandi, pasang perlindungan brute-force (`sudo apt install fail2ban`).
- Utamakan kunci SSH; simpan kata sandi untuk `sudo`.
- Simpan kata sandi baru di password manager, tidak pernah di file di server.
