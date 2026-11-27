# UPD-01: update from the panel

`nostek-update.sh` is started by the agent (Settings → Update in the panel) as its own systemd unit. It updates the
agent (SITE-01 APPLY) and the panel (PANEL-01 UPDATE) to a release, checks both (VERIFY) and puts the previous
release back on any failure. Design: [docs/design/upd-01.md](../../docs/design/upd-01.md).

```
bash nostek-update.sh RUN --ack UPD-01 --tag vX.Y.Z --commit SHA --expect-ip ADDR --expect-host NAME \
  --domain PANEL_DOMAIN --preview-zone ZONE [--db-copy FILE]
bash nostek-update.sh STATUS
```

The agent runs the copy of the release that runs now, after checking it against `upd-01.lock`. Tests:
`sudo bash tests/harness.sh`.
