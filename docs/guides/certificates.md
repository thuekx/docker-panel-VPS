# Certificates: automatic, own and wildcard

Every website name gets a free **Let's Encrypt** certificate by itself, as soon as its DNS points to this server.
Nothing has to be done for that. This guide is for the other cases.

## Own certificate (one website)

Use it for a Cloudflare origin certificate behind the Cloudflare proxy ("Full (strict)"), a certificate bought
elsewhere, or one from a company CA.

1. Open the website, then **Domains → Certificate → Use an own certificate**.
2. Paste the certificate in PEM (the website's certificate first, then its chain) and its private key in PEM.
3. **Save certificate**. The agent checks that the key belongs to the certificate, that it is valid now and for at
   least one more day, and that it covers the website's main name. The edge uses it at once.

Names of the website the certificate does not cover keep their Let's Encrypt certificate. A certificate no public CA
trusts (a Cloudflare origin certificate is one) is accepted with a warning: browsers that come straight to the server
do not trust it. **Remove own certificate** goes back to Let's Encrypt.

The key is kept only on the server (`tls/key.pem` in the website folder and a copy for the edge, both readable by
root and the edge only). It is never shown again and is not in any log. It moves with the website's backups, the
trash and a server backup. An alert of the kind **Own certificate ending** comes 14 days before the end
(Settings → Alerts).

## Certificate request (CSR): a certificate you buy

Use this when a certificate authority (Sectigo, DigiCert, GlobalSign, a reseller) asks for a CSR. The private key
is made on this server and never leaves it.

1. **Domains → Certificate → Make a certificate request (CSR)**.
2. Tick the names. The main name must be one of them, and `*.domain` is offered for a wildcard. Choose the key:
   ECDSA P-256 is recommended, and RSA is there for authorities that want it.
3. Domain-validated certificates need only the names. For an organisation-validated certificate, fill in the
   organisation, city, province and country exactly as registered.
4. **Make request**. Copy the request, from `-----BEGIN CERTIFICATE REQUEST-----` to the END line, into the
   authority's order form.
5. When the certificate comes, open **Use an own certificate**, paste it with its chain, and **leave the private key
   empty**. The agent pairs it with the key it made. The request is then done and removed.

A new request replaces the waiting one, and its key is deleted. **Delete request** throws the request and key away.
The waiting key is kept in `tls/request/` in the website folder (readable by root only) and moves with backups.

## Self-signed certificate: test names and private networks

**Self-signed certificate** makes a certificate signed by its own key, for the ticked names, valid 30 to 825 days.
The edge serves it at once. Browsers show a warning for it, so use it only for test names, a private network, or
behind a proxy that does not check the certificate. Replace it with **Use an own certificate**, or go back to
Let's Encrypt with **Remove own certificate**.

## Wildcard certificate through ClouDNS

A wildcard (`*.example.com`) is one certificate for a zone and every name one level below it. Let's Encrypt gives a
wildcard only through the DNS check: the edge adds a short-lived TXT record `_acme-challenge` to the zone and removes
it again. That needs an API sub-user of ClouDNS, where the zone is kept. The free ClouDNS plan may not include API
access; check under **API & Resellers** in your ClouDNS account.

### Set up the sub-user once

1. In ClouDNS open **API & Resellers → API sub-users** and add a sub-user. Limit it to the zones that need a wildcard.
   If ClouDNS asks for allowed IP addresses, add this server's public address.
2. In the panel open **Settings → Wildcard certificates (ClouDNS)**, paste the sub-auth-id and the password, then
   **Test and save**. The panel asks ClouDNS to log in with them first; a refusal shows ClouDNS's own words.
3. The edge restarts for a few seconds to read the new sub-user: every website and the panel may not open for a
   moment.

The password is kept only in `/srv/nostek/edge/dns/` (readable by root and the edge), never in the panel's
database, a page, the compose file or a log. It is not part of a server backup: set it again on a new server.

### Use it for a website

On the website's **Domains** page, under **Wildcard certificate**, choose the zone (the main name or a zone above it)
and **Use the wildcard**. The agent asks ClouDNS whether the sub-user can see that zone, then puts the website's names
in that zone on the wildcard. Its other names (for example the preview address, or names two levels down) keep
Let's Encrypt over HTTP. The first wildcard takes about a minute; the Domains page shows the certificate once it is
there. **Turn the wildcard off** goes back to one certificate per name.

The sub-user cannot be removed while a website uses a wildcard; the settings page names those websites.
A website on its preview address cannot take a wildcard until its own domain is the main address.
