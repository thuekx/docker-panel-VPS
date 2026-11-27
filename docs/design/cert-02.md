# CERT-02: certificate request and self-signed certificate (v1.44.0)

Roadmap 15f (RAYX-2). This builds on CERT-01 (an own certificate per website in `tls/`, served by the edge's file
provider).

## Certificate request (CSR)
- `CreateCSR` checks the names, then makes the key and the request.
- **Names**: each name is one of the website's own names (not the preview address) or `*.` plus one of them. Names
  are lowercased and de-duplicated, with at most 20.
  - They must include the main name (or, while the main name is the preview address, one own name). This is the
    same rule `SetCert` applies later, so a certificate issued for the request can always be used.
- **Key**: `ec256` (default), `rsa2048` or `rsa3072`, as PKCS#8.
  - It is written to `tls/request/key.pem` (0600; `tls/` 0700) and is never answered or logged.
- **Subject**: CN is the first name. Organisation, unit, city and province are optional (at most 64 letters, digits
  or `.,&'()/_-`), and the country is two letters.
- **CSR**: `tls/request/csr.pem` (public), answered with its names, key type and creation time.
- A new request replaces the waiting one. `DeleteCSR` removes `tls/request/`.
- **Pairing**: when `SetCert` gets a certificate with an empty key, it uses the waiting key, but only when the key
  belongs to the certificate. Otherwise the error says the key is needed. All CERT-01 checks still run. On success
  `tls/request/` is removed.
- `RemoveCert` now removes only `cert.pem` and `key.pem`, so a waiting request survives going back to Let's Encrypt.

## Self-signed
- `SelfSigned(names, key, days)`:
  - names as above; days from 1 to 825 (the browser limit for leaf certificates);
  - key `ec256` or `rsa2048`;
  - serial: 120 random bits; NotBefore: now − 5 min;
  - subject O "Nostek Panel self-signed"; EKU serverAuth;
  - stored and served through `SetCert`.
- `CertInfo.self_signed` is true when the issuer equals the subject and the certificate's own key verifies its
  signature. The panel then shows "Self-signed" with a warning in place of "Not publicly trusted".

## Agent API
- `GET /v1/sites/{id}/cert/csr`: the waiting request, with `csr` empty when there is none.
- `POST /v1/sites/{id}/cert/csr`: `{names, key, org, unit, city, state, country}`.
- `POST /v1/sites/{id}/cert/csr/delete`
- `POST /v1/sites/{id}/cert/self-signed`: `{names, key, days}`.
- `PUT /v1/sites/{id}/cert` with `"key":""`: pairs the certificate with the waiting key.

The audit records names, key type and end date, never key material.

## Panel
In Domains → Certificate:
- a waiting-request box shows the CSR (read only), its names, key type and time, and **Delete request**;
- **Make a certificate request (CSR)** has name checkboxes, the key type and the subject fields;
- **Self-signed certificate** has names, the key type and days;
- the upload form no longer requires a key while a request is waiting.
