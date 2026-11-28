---
id: NP-TS-010
tier: user
keywords: sibuk, busy, berjalan, sedang berjalan, tunggu, menunggu, tombol mati, disabled, another change, perubahan lain
errors: busy
---

# Buttons are greyed out: another change is running

## Symptom
Buttons of a website are greyed out, the page says the website is busy, or an action answers "another change is
running; try again when it is done".

## Cause
A website takes one change at a time, so two changes never write over each other. While a create, deploy, backup,
restore, migration, update or image update runs, other changes of that website wait.

## How to check
1. Open the website's overview: the running job and its steps are shown at the top.
2. The page updates by itself. Most changes take seconds; a backup or restore of a large website can take minutes.

## Fix
Wait until the job says done or failed, then press the button again. Nothing has to be undone: the change you tried
was not started.

## Still stuck?
If the same job runs for more than an hour, send support the article code **NP-TS-010**, the website's domain, the
job's name and when it started (with the time zone).
