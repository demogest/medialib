# Security policy

## Reporting a vulnerability

Please do not open a public issue for a security problem. Report it privately instead: **[Report a vulnerability](https://github.com/demogest/medialib/security/advisories/new)** (the repository's **Security** tab → **Report a vulnerability**). Only the maintainers see the report, and the fix is worked out with you in private.

A useful report says:

- what someone could do, and from where (another computer on the network, a web page in the user's browser, a file in a library or bucket);
- the version (Settings → About, or `medialib version`) and the system;
- how medialib was running: the desktop app or `medialib serve`, with `--host`, with or without a password, behind a reverse proxy or not;
- the steps that show it, or a proof of concept.

What happens next:

- You hear back within a few days.
- The fix is released as a patch version as soon as it is ready. It does not wait for the scheduled bug-fix release, and the app offers it to everyone through its updates.
- A GitHub security advisory is published with the release. It credits you, unless you would rather not be named.

## Supported versions

Only the latest release gets security fixes. medialib looks for new releases by itself (Settings → Automatic updates), so keep that on, or update a server's program or image when it says there is a new version. Alpha and beta versions get no fixes of their own: the next one replaces them.

## What counts

The protections medialib promises are listed in the README's [Security](README.md#security) section. A way around any of them is in scope, for example:

- reaching storage, connections or anything that changes state from another computer without the password, including through a reverse proxy;
- making medialib run a program, or open something in a player or a dialog, on the computer running it, from anywhere but that computer;
- reading files outside the libraries, or credentials from `config.json`, through the server;
- a page on another site acting on medialib through the user's browser (cross-site requests, DNS rebinding, framing);
- a file served from a library or a bucket running as part of medialib;
- the updater installing anything other than the release's own files, matching its `SHA256SUMS`.

Out of scope:

- someone who can already use the computer running medialib, or read its `config.json`;
- what other computers may do by design on a server opened with `--host` and no password: browse the libraries and watch;
- a signed-in user slowing the server down;
- problems in ffmpeg, a player or an S3 store themselves: report those to their makers.
