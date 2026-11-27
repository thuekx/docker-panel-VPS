# Guide: change the default VPS user and SSH port safely (GUIDE-01)

For Ubuntu 22.04 / 24.04 servers created by a VPS provider with a default user (often `ubuntu`, `root` or
similar). You end up with your own user, an SSH port of your choice, and the old user locked (not deleted).

Placeholders, replace them everywhere:

| Placeholder | Example | Meaning |
|---|---|---|
| `NEWUSER` | `alice` | Your new login user |
| `NEWPORT` | `22022` | New SSH port (1024-65535, not used by anything else) |
| `OLDUSER` | `ubuntu` | User the provider created |
| `SERVER` | your server address | Server IP or host name |

> **Golden rule:** keep your current SSH session open until step 6 is confirmed from a *new* session.
> Port 22 stays open until the new port is proven to work.

## Step 0 — before you start (provider console)

1. Take a **snapshot** of the VPS in the provider's console.
2. If the provider has its own firewall / security group, **allow TCP `NEWPORT`** there (keep 22 open for now).

## Step 1 — create the new user (as OLDUSER)

```bash
sudo adduser NEWUSER            # asks for the new password twice (hidden)
sudo usermod -aG sudo NEWUSER
```

If you log in with an SSH key, copy it to the new user:

```bash
sudo install -d -m 700 -o NEWUSER -g NEWUSER /home/NEWUSER/.ssh
sudo install -m 600 -o NEWUSER -g NEWUSER ~/.ssh/authorized_keys /home/NEWUSER/.ssh/authorized_keys
```

## Step 2 — open the new SSH port next to port 22

Ubuntu reads `/etc/ssh/sshd_config.d/*.conf` in name order and the **first** value wins, so use a name that
sorts before the provider's files (for example `10-…`).

```bash
printf 'Port 22\nPort NEWPORT\n' | sudo tee /etc/ssh/sshd_config.d/10-custom-ssh.conf
# only if you want password login for NEWUSER:
echo 'PasswordAuthentication yes' | sudo tee -a /etc/ssh/sshd_config.d/10-custom-ssh.conf
sudo sshd -t && echo "SSHD_CONFIG_OK"
sudo ufw status | grep -q active && sudo ufw allow NEWPORT/tcp
```

## Step 3 — safety net, then apply

Schedule an automatic undo in 10 minutes (cancelled in step 5 if all is well):

```bash
sudo systemd-run --unit=ssh-revert --on-active=10min \
  sh -c 'mv /etc/ssh/sshd_config.d/10-custom-ssh.conf /root/10-custom-ssh.conf.reverted; systemctl daemon-reload; systemctl restart ssh.socket 2>/dev/null; systemctl restart ssh'
```

Apply the new port:

```bash
# Ubuntu 24.04 (SSH started by systemd socket):
sudo systemctl daemon-reload && sudo systemctl restart ssh.socket
# Ubuntu 22.04:
sudo systemctl restart ssh
sudo ss -tlnp | grep -E ':(22|NEWPORT) '      # both ports must be listed
```

## Step 4 — test from a NEW terminal (keep the old one open)

```bash
ssh -p NEWPORT NEWUSER@SERVER
sudo -v && echo "SUDO_OK"
```

## Step 5 — confirm (in the new session)

```bash
sudo systemctl stop ssh-revert.timer && echo "REVERT_CANCELLED"
```

If step 4 failed, do nothing: after 10 minutes the server goes back to the old settings by itself.

## Step 6 — close port 22

```bash
sudo sed -i '/^Port 22$/d' /etc/ssh/sshd_config.d/10-custom-ssh.conf
sudo sshd -t && echo "SSHD_CONFIG_OK"
sudo systemctl daemon-reload && (sudo systemctl restart ssh.socket 2>/dev/null; sudo systemctl restart ssh)
sudo ufw status | grep -q active && sudo ufw delete allow 22/tcp ; sudo ufw delete allow OpenSSH 2>/dev/null
```

Log in once more from a new terminal on `NEWPORT`, then remove port 22 from the provider firewall.

## Step 7 — lock the old user (do not delete it)

```bash
sudo passwd -l OLDUSER
sudo usermod -s /usr/sbin/nologin OLDUSER
sudo mv /home/OLDUSER/.ssh/authorized_keys /home/OLDUSER/.ssh/authorized_keys.disabled 2>/dev/null
```

## Recommended afterwards

- With password login, install brute-force protection (`sudo apt install fail2ban`).
- Prefer SSH keys; keep the password for `sudo`.
- Store the new password in your password manager, never in a file on the server.
