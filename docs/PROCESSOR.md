# Processor: run a program on tagged notes, reply by AMail

`amail process` turns an AMail node into a little request/response service.
It watches the node's inbox for notes whose **first line** starts with a
handler tag, runs the handler's command with the request on stdin, and
sends the command's output back as a new note to another node. Combined
with the e-mail gateway that gives a loop like this:

```
phone: e-mail to info@example.com
       Subject: #amail austin-pc #claude What is the capital of France?
                 |
                 v  gateway (e-mail -> AMail) on the server node
       outbox/austin-pc/What is the capital of France-<ts>/message.txt
           "#claude What is the capital of France?"
                 |
                 v  AMail delivers to the PC
       inbox/ausa-web/.../message.txt
                 |
                 v  amail process on the PC: handler "#claude" runs claude-headless.cmd
       outbox/ausa-web/claude-reply-<ts>.txt
           "#email Claude: What is the capital of France?"
           "Paris is the capital of France. ..."
                 |
                 v  AMail delivers to the server; gateway (AMail -> e-mail) sends it
       e-mail to you: Subject "Claude: What is the capital of France?"
```

## Note format

```
#claude <request text, optional>
<blank line>
<more request text, optional>
```

The tag is the first word of the first non-empty line. The rest of that
line plus the body is the request. Attachments in the same folder are
ignored (processors are text in, text out).

## Config

`amail process --init --config /path/processor.json` writes an example:

```json
{
  "inbox": "C:\\Users\\you\\AMail\\inbox",
  "outbox": "C:\\Users\\you\\AMail\\outbox",
  "allowed_origins": ["ausa-web"],
  "handlers": [
    {
      "tag": "#claude",
      "command": ["cmd.exe", "/c", "C:\\Users\\you\\AppData\\Local\\Programs\\AMail\\claude-headless.cmd"],
      "reply_to": "ausa-web",
      "reply_first_line": "#email Claude: {subject}",
      "timeout_seconds": 300,
      "max_input_kb": 64,
      "max_output_kb": 512
    }
  ],
  "poll_seconds": 30,
  "stable_seconds": 5,
  "report_errors": true,
  "state": "C:\\Users\\you\\.amail\\processor.json.state"
}
```

* `allowed_origins`: only notes from these nodes run anything. Required.
* `handlers[].command`: an argv list, never taken from the message. The
  request arrives on stdin; stdout becomes the reply.
* `handlers[].reply_to` / `reply_first_line`: where the reply goes and its
  first line; `{subject}` is the text after the tag (first 80 characters),
  `{origin}` the sender node. Start it with `#email ...` on a node that runs
  the outbound gateway and the answer goes out by e-mail.
* Each note is processed exactly once, even if the command fails: commands
  may have side effects, so there are no retries. With `report_errors` the
  failure (exit code, stderr, timeout) is sent back as the reply instead.
* Size caps on input and output, a timeout per handler, the reply carries
  the original request as a footer.

## The Claude handler on Windows

`scripts/windows/claude-headless.cmd` runs the Claude Code CLI that ships
with the Claude desktop app (or a `claude` on PATH) in print mode with
**all tools disabled** (`--tools ""`), one turn, no session saved, in an
empty working folder, and a system prompt that says it is answering an
e-mailed question from knowledge only. So a request that arrives by e-mail
can never make Claude read, write or run anything on the PC: it can only
produce text.

One-time setup, in your own PowerShell window (the scheduled task then
inherits the login):

```powershell
irm https://claude.ai/install.ps1 | iex     # or: npm install -g @anthropic-ai/claude-code
claude                                      # then type /login, finish in the browser, then /exit
```

The wrapper looks in `%USERPROFILE%\.local\bin`, `%APPDATA%\npm`, the
desktop app's bundle folder and PATH, in that order.

## Running it

```powershell
# try by hand
amail process --config C:\Users\you\.amail\processor.json --once

# keep it running (this user's logon only, hidden, no wake, restart on crash)
powershell -ExecutionPolicy Bypass -File scripts\windows\install-processor-task.ps1 -Config C:\Users\you\.amail\processor.json
```

The log is `processor.log` in the node home. On Linux run it as a systemd
service next to the node, the same way as the gateway.

## Security notes

* The processor is a remote code path by design: whoever can get a tagged
  note into the inbox from an allowed origin gets the handler's command run
  with their text on stdin. Keep `allowed_origins` to nodes you control, keep
  handler commands sandboxed (the Claude wrapper disables tools), and
  remember the inbound gateway's own gates (sender allow-list, SPF/DKIM,
  optional secret) sit in front of this when requests come by e-mail.
* Handlers must not interpret the request as instructions to the machine.
  Text in, text out.
