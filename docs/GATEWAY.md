# E-mail gateway: from an inbox to an AMail node

`amail gateway` lets you send something into the AMail network from any
mail client: put `#amail <node-id>` in the subject, attach what you like,
send it to a mailbox the gateway watches. A minute or so later the note and
the attachments are in that node's inbox folder.

```
phone / any mail client
   Subject: Site photos #amail austin-pc for the new page
   Body: two shots from today
   Attachments: IMG_0812.jpg, IMG_0813.jpg
         |
         v  (SMTP)
mail provider  ->  Maildir on a machine that runs an AMail node
         |         (Amazon SES + S3 + sync script, Dovecot, fetchmail, getmail ...)
         v
amail gateway   reads new mail, checks sender + SPF/DKIM, writes
         |
         v
outbox/austin-pc/Site photos for the new page-20260917-103000/
    message.txt        "two shots from today"
    headers.txt        From, Date, Subject, Message-ID, Authentication-Results
    IMG_0812.jpg
    IMG_0813.jpg
         |
         v  (the AMail node delivers it like any other outbox folder)
austin-pc: inbox/<this-node>/Site photos for the new page-20260917-103000/
```

## Subject format

```
[anything] #amail <node-id> [secret] [anything]
```

* `#amail` may appear anywhere in the subject; matching is case-insensitive.
  The tag is configurable (`tag`), and must start with `#` so ordinary
  subjects never match.
* `<node-id>` is the destination, a node id like `austin-pc`. It must be on
  the sending node's whitelist, or the file ends up in `failed/`.
* `[secret]`: if the gateway config has a `secret`, the word right after the
  node id must be it. Everything else in the subject becomes the message
  title (the folder name), or `email` if nothing is left.

## Who may send

The gateway refuses anything that fails **all** of these:

1. **Sender allow-list**: the `From` address (or its `@domain`) is in
   `allowed_senders`. The list must not be empty; an open gateway would let
   anyone put files on your machines.
2. **Authentication**: with `require_auth` (default true) the message must
   carry `spf=pass` or `dkim=pass` in an `Authentication-Results` header,
   or a passing `Received-SPF`, as judged by *your* receiving mail server
   (Amazon SES, Gmail and most providers add these). This is what stops a
   stranger from simply typing your address into the From field.
3. **Optional secret** in the subject, for a second factor that does not
   depend on the mail provider.
4. If SES verdict headers are present (`X-SES-Spam-Verdict`,
   `X-SES-Virus-Verdict`), they must be `PASS`.
5. Size: messages over `max_mb` (default 25) are rejected.

Rejected and converted messages are logged with the reason; ordinary mail
without the tag is ignored silently. The gateway **never deletes or moves
mail**; it remembers what it has handled in a state file (by Maildir id, so
a message stays recognised after your mail reader moves it to `cur/`).

Remember the rest of the security model: the note and attachments then
travel through AMail like any file. The node whose outbox the gateway writes
into is the *origin* of the message, and it signs it as such.

## Config

`amail gateway --init --config /path/amail-gateway.json` writes an example:

```json
{
  "maildir": "/home/you/Mail/example.com/info",
  "outbox": "/home/amail/AMail/outbox",
  "allowed_senders": ["you@example.com", "@yourcompany.example"],
  "require_auth": true,
  "tag": "#amail",
  "secret": "",
  "max_mb": 25,
  "poll_seconds": 30,
  "state": "/path/amail-gateway.json.state"
}
```

`poll_seconds: 0` (or `--once`) scans a single time and exits, handy for a
cron job or a systemd timer chained after your mail sync.

## Running it

The gateway needs to read the Maildir and write the node's `outbox`. On a
box where the AMail node runs as a dedicated user (`amail`) and mail
belongs to your login user, run the gateway **as your login user**: the
installer made you a member of the `amail` group and the outbox is
group-writable, so the files land with the right ownership. No node keys
are involved; the gateway only drops files where the node will find them.

```bash
# test by hand
amail gateway --config ~/.config/amail-gateway.json --once

# keep it running
sudo cp scripts/linux/amail-gateway.service /etc/systemd/system/
sudo sed -i 's/CHANGEME/youruser/g' /etc/systemd/system/amail-gateway.service
sudo systemctl daemon-reload && sudo systemctl enable --now amail-gateway
journalctl -u amail-gateway -f
```

## What the receiver sees

One folder per e-mail in `inbox/<gateway-node>/`, named after the subject
with a timestamp, holding `message.txt` (or `message.html` when the mail had
no plain-text part), `headers.txt` and the attachments with their original
names (sanitised for the receiving OS; duplicates get ` (2)`). In the web UI
it shows as one grouped message.

## Limits and notes

* Inline images referenced by HTML are saved as attachments; the HTML is
  kept only when there is no plain-text alternative.
* Nested `message/rfc822` forwards are treated as attachments (`.eml`), not
  unpacked.
* The gateway is a one-way door: e-mail into AMail. Nothing goes back out
  by e-mail.
