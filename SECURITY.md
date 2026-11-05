# Security Policy

## Supported Versions

The go-coap v3 release line is currently supported.

| Version        | Supported |
|----------------|-----------|
| v3.x           | Yes       |
| v2.x and older | No        |

Security fixes are developed against the latest released version of the v3 release line.

Backporting security fixes to older releases is not guaranteed. Users should upgrade to the latest available v3 release to receive security updates.

This policy will be updated if the project's supported release lines change.

## Reporting a Vulnerability

The go-coap maintainers take security vulnerabilities seriously.

**Please do not report security vulnerabilities through public GitHub issues.**

If you discover a potential security vulnerability in go-coap, please report it privately using one of the following methods:

- **GitHub Security Advisories:** https://github.com/plgd-dev/go-coap/security/advisories/new
- **Email:** security@plgd.dev

Please include as much information as possible to help us understand and reproduce the issue.

A report should ideally contain:

- A description of the vulnerability.
- Affected go-coap versions, packages and components.
- Potential security impact.
- Steps to reproduce the issue.
- A proof of concept or minimal reproducer, if available.
- Any suggested mitigation or fix.

Please allow the maintainers reasonable time to investigate and address the issue before making it public.

## Scope

The following types of security issues are particularly relevant to go-coap:

- Remote code execution.
- Denial of service caused by malformed or malicious CoAP messages.
- Memory exhaustion or uncontrolled resource allocation.
- Integer overflows and unsafe message length handling.
- Out-of-bounds access and unexpected panics triggered by remote input.
- Authentication or transport security bypasses.
- TLS and DTLS configuration or implementation issues.
- Information disclosure between clients or sessions.
- Race conditions affecting security-sensitive operations.
- Vulnerabilities in CoAP message parsing, option handling, Block-wise transfers and Observe functionality.

Reports should demonstrate a realistic security impact and an applicable attack scenario.

General protocol interoperability issues, RFC compliance problems without security impact, and issues that are entirely within the responsibility of applications using go-coap are generally outside the security vulnerability scope.

## Response and Disclosure Process

The go-coap maintainers aim to handle security reports promptly and transparently.

We follow these response targets:

- **Acknowledgement:** We aim to acknowledge receipt of a vulnerability report within 5 business days.
- **Initial assessment:** We aim to provide an initial assessment within 10 business days.
- **Status updates:** We aim to keep the reporter informed about investigation progress and remediation plans.
- **Target disclosure timeline:** We aim to resolve and disclose reported vulnerabilities within 90 calendar days of the initial report.

These are target timelines rather than guaranteed deadlines. Complex vulnerabilities may require additional investigation or coordination.

## Coordinated Disclosure and Disclosure Deadlines

We request that reporters keep vulnerability details confidential while a fix or mitigation is being developed.

The reporter and maintainers should coordinate a reasonable disclosure date, taking into account the vulnerability severity, affected users and availability of a fix.

The default disclosure target is 90 calendar days from the initial report.

If the maintainers do not acknowledge the report, fail to provide meaningful updates, or are unable to develop a fix or mitigation within a reasonable timeframe, the reporter may proceed with coordinated public disclosure after the 90-day period.

If additional time is necessary, the maintainers should communicate the reason and propose a revised disclosure date. Any extension should be agreed upon with the reporter.

For actively exploited vulnerabilities or situations presenting an immediate risk to users, an earlier disclosure timeline may be appropriate.

If the maintainers become unresponsive, the reporter is encouraged to make reasonable attempts to contact them through the alternative reporting channel listed above before proceeding with public disclosure.

The maintainers will make reasonable efforts to coordinate disclosure, publish a security advisory and provide a patched release when appropriate.

Reporters will be credited in security advisories when requested and appropriate.

## Security Advisories

Published security advisories and vulnerability information are available at:

https://github.com/plgd-dev/go-coap/security/advisories

## Dependencies

Security issues affecting third-party dependencies should generally be reported to the respective upstream maintainers.

However, if a vulnerability in a dependency creates a demonstrable security issue in go-coap through its integration or usage, please report it to the go-coap maintainers as described above.
