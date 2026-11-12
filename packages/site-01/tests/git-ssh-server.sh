#!/bin/sh
# A throwaway Git server over SSH for the site-01 harness (APP-02b private repositories). It accepts only the
# deploy key in NP_PUB, as user git with git-shell, and serves /srv/app.git built from /app-src.
set -e
apt-get update -qq >/dev/null && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq openssh-server >/dev/null
useradd -m -s /usr/bin/git-shell git && usermod -p '*' git
mkdir -p /home/git/.ssh && printf '%s\n' "$NP_PUB" > /home/git/.ssh/authorized_keys
chown -R git:git /home/git/.ssh && chmod 700 /home/git/.ssh && chmod 600 /home/git/.ssh/authorized_keys
git init -q --bare -b main /srv/app.git
work="$(mktemp -d)" && cp -R /app-src/. "$work/" && cd "$work" && git init -q -b main && git add . &&
  git -c user.email=harness@example.test -c user.name=harness commit -qm "private app" && git push -q /srv/app.git main
chown -R git:git /srv/app.git
mkdir -p /run/sshd && ssh-keygen -A >/dev/null
echo "np-git-ssh ready"
exec /usr/sbin/sshd -D -e
