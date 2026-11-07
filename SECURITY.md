# Security

Please report vulnerabilities privately, not in public issues: use
**Report a vulnerability** on this repository's Security tab (GitHub's
private vulnerability reporting). Tell us what is affected, how to
reproduce it and what an attacker gets.

What matters most: remote code execution, account takeover, bypassing
authentication, and leaks of other users' messages or keys.

We answer within a week and fix what we can confirm as fast as we can. Once
a fix is out we credit you in the release notes unless you'd rather not be
named. When a fix warrants one, we request a CVE for it.

In scope: this server and the web client patch in `web/`. Out of scope:
Telegram's own apps and services, and the mini apps captured from Telegram
(report those to Telegram).

Only the latest commit on `main` gets fixes. If you run an instance, keep it
up to date and keep `GREENGRAM_DATA_DIR` (keys, uploads) and the database
private.
