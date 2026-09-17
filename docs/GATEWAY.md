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

## The other direction: AMail to e-mail

The same gateway can send. Put a text file into an AMail node whose
**first line** is

```
#email [to:someone@example.com] [style:geex] Subject text here
```

and, when it arrives in the gateway node's inbox, the rest of the file is
sent as the e-mail body with that subject. Use it from your PC by writing a
note in Compose (or dropping a `.txt` into `outbox/<gateway-node>/`) that
starts with `#email ...`. If the note has attachments (a message folder with
`message.txt`), every other file in the folder is attached to the e-mail;
`headers.txt` is skipped.

Rules, mirroring the inbound side:

* Only nodes in `email_origins` may trigger mail; anyone else's `#email`
  file is refused (and told so by receipt).
* Recipients are `email_to` unless the first line carries `to:addr`, and an
  override must be on `email_allowed_to` (default: the same as `email_to`).
* `style:NAME` on the first line sends the mail as **text plus HTML**
  (`multipart/alternative`): the text exactly as written, and an HTML part
  rendered from a template with every style inline, which is what Outlook.com
  and Gmail actually display. The built-in style `geex` follows the Geex
  admin theme (Poppins, purple `#AB54DB`, grey `#F3F2F7`): a card with the
  subject as title, the body, and a footer naming the sender and the origin
  node. The body is treated as light Markdown: headings, `-`/`1.` lists,
  fenced code, `inline code`, **bold**, *italic*, links, `>` quotes, simple
  `|` tables and `---` rules; everything is HTML-escaped first. Add your own
  templates under `email_styles` (`{"name": "/path/template.html"}`, Go
  `html/template` with `.Subject .Origin .From .Date .Body .Text`; `.Body` is
  the rendered HTML) and set `email_default_style` to style every mail. An
  unknown style refuses the mail with a receipt saying so. This is how
  Claude's answers arrive nicely formatted: the processor's reply line is
  `#email style:geex Claude: {subject}` (see `docs/PROCESSOR.md`).
* `email_max_mb` (default 7) caps body plus attachments; Amazon SES refuses
  raw messages over 10 MB.
* The message is handed on stdin to `send_command` with the recipients as
  arguments, sendmail style. `ses-send.sh` (SES with an instance role),
  `msmtp`, `/usr/sbin/sendmail` all fit. The gateway holds no credentials.
* A receipt lands in `outbox/<origin>/email-receipt-<time>.txt`, so the
  sender sees "sent, id ..." or "NOT sent, reason ..." in their inbox.
* A failed send (command error) is retried on the next poll; a refused one
  (bad recipient, too big, wrong origin) is not.
* `email_delete_after_send` removes the source from the inbox after a
  successful send; default false, so the gateway node keeps a copy.

Config keys for this side, in the same JSON:

```json
{
  "inbox": "/home/amail/AMail/inbox",
  "email_from": "info@example.com",
  "email_to": ["you@example.com"],
  "email_allowed_to": ["you@example.com", "friend@example.net"],
  "email_origins": ["your-pc"],
  "send_command": ["/home/you/Tools/ses-send.sh"],
  "email_max_mb": 7,
  "email_receipt": true,
  "email_delete_after_send": false,
  "email_styles": {},
  "email_default_style": ""
}
```

Either side can be configured alone: leave out `maildir` for send-only,
leave out `inbox`/`send_command` for receive-only.

## Limits and notes

* Inline images referenced by HTML are saved as attachments; the HTML is
  kept only when there is no plain-text alternative.
* Nested `message/rfc822` forwards are treated as attachments (`.eml`), not
  unpacked.
* The gateway is a one-way door: e-mail into AMail. Nothing goes back out
  by e-mail.
