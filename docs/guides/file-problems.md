---
id: NP-TS-028
tier: user
keywords: file, upload, unggah, terlalu besar, too large, nama, sudah ada, exists, file manager
errors: file
---

# A file cannot be uploaded, made or renamed

## Symptom
The File Manager answers "The upload is too large", "Name is not valid", "No file chosen yet", "already exists" or
"choose another name".

## Cause
- One upload is larger than the panel accepts.
- A name holds `/`, or is `.` or `..`.
- A file or folder with that name is already in the folder.

## How to check
Look at the size of the file and at the names in the folder you are in.

## Fix
- Too large: pack the files in a .zip and upload that (the File Manager can unpack it), or upload over SFTP.
- Name: choose a name without `/`.
- Exists: rename or delete the old one first, or choose another name.

## Still stuck?
Send support the article code **NP-TS-028**, the website's domain, the folder and the file name.
