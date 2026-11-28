---
id: NP-TS-011
tier: user
keywords: agent, agen, not reachable, tidak terjangkau, tidak terhubung, status unknown, tidak diketahui, website tidak tampil
errors: noagent
---

# The panel cannot reach its agent

## Symptom
Websites do not show, their status is "unknown", buttons answer "agent not reachable", or the new-website form says
the agent is not reachable.

## Cause
The panel shows and asks; a service on the server, the **agent**, makes every change to websites. When the agent is
stopped, restarting or overloaded, the panel cannot reach it. Websites themselves keep running: only the panel
cannot see or change them for a moment.

## How to check
1. Wait half a minute and reload the page: after an update or a restart the agent is back within seconds.
2. Admins: open **Settings → System**. The agent is listed under server services with its state.

## Fix
- Admins: press **Restart** next to the agent on the System page.
- Hosting accounts: tell the server's admin; nothing on your websites has to be changed.

## Still stuck?
Send support the article code **NP-TS-011**, the incident code shown under the error, and the time (with the time
zone).
