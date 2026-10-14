# Security Policy

## Reporting a vulnerability

Please do not open a public issue for security problems.

Email **jody@seabeam.net** with a description of the issue, the affected
version or commit, and steps to reproduce. You will get an acknowledgement
within three business days and a fix or a mitigation plan as soon as we have
one. We will credit you in the release notes unless you ask us not to.

## Scope

tacplus is a code generator. A security issue is anything that makes the
*generated* code unsafe (for example a bypass in generated authorization,
validation, redaction or token handling), as well as anything in the compiler
itself that could be exploited by a crafted proto source.

## Supported versions

Only the latest release receives security fixes.
