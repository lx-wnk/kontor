# Security Policy

## Supported Versions

Kontor is in active pre-1.0 development. Only the latest commit on
`main` (and the most recent published release, if any) receives security fixes.
Older releases are not supported.

| Version       | Supported |
|---------------|-----------|
| `main` (HEAD) | Yes       |
| All others    | No        |

## Threat Model

Kontor is a **local-first** tool. The server binds exclusively to
`127.0.0.1` and reads sensitive Claude Code session data from your local
filesystem. It is not designed to be exposed to the public internet. See
[docs/guides/security.md](docs/guides/security.md) for a full description of
the security model, network binding rules, and the multi-machine remote mode.

Authentication is **off by default** (`auth.mode = none`): on the loopback
interface, any local process with a matching `Origin` header reaches the full
API, including routes that define the command a spawner executes. This is a
deliberate single-user local-trust posture, not an oversight — see
[Authentication and the local-trust default](docs/guides/security.md#authentication-and-the-local-trust-default)
for what it permits and when it is the wrong posture for you.

## Reporting a Vulnerability

**Please do not open a public GitHub issue for security vulnerabilities.**

Report vulnerabilities privately via GitHub Security Advisories:

> https://github.com/lx-wnk/kontor/security/advisories/new

Include a description of the issue, reproduction steps, and (if known) the
potential impact. You will receive a response as soon as possible. If the report
is confirmed, a fix will be prepared and a coordinated disclosure will be
arranged before any public announcement.
