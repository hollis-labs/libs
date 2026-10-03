# Security policy

## Supported versions

Every module in this repository is pre-1.0 and versioned on its own. Security
fixes are made on the current development line and in the newest tagged release of
the affected module. Older releases may not receive backports. Check a module's
`CHANGELOG.md` for what changed and when.

## Report a vulnerability

Do not include an exploit, token, credential, private key or other sensitive
material in a public issue.

Use GitHub's private vulnerability-reporting flow when the repository's Security
tab offers it. If it is unavailable, contact a repository maintainer privately
through a contact channel published on the Hollis Labs organization or maintainer
profile. Include:

- the affected module, version or commit, and the Go version and operating system
- how the module was used: which packages, and whether untrusted input reached it
- reproduction steps and the security impact
- whether credentials or user data may have been exposed
- a safe way to contact you about coordination

Maintainers will acknowledge a private report, investigate it, and coordinate
disclosure and a fix release with you. Response times are best effort while the
project is pre-1.0.

## Scope

The modules here are Go libraries, not services. They open no listener of their
own, and how an application deploys, authenticates and isolates its use of them is
that application's responsibility. A vulnerability is in scope when a module
behaves unsafely on input an application could reasonably pass it, for example
the queue, scheduler, SQLite, SFTP sync or web UI code.

## Current limitations

- Every module is pre-1.0: APIs and behavior may change in any minor release.
- The modules are skeletons while existing code is imported; reports against code
  that has not landed here belong in the repository the code still lives in.

These are statements of the current state, not hidden promises. Operate within them.
