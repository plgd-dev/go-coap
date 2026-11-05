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

The maintainers will:

1. Acknowledge receipt of a vulnerability report as soon as reasonably possible.
2. Investigate and assess the reported security impact.
3. Determine affected versions and identify possible mitigations.
4. Develop and test a security fix where appropriate.
5. Coordinate disclosure with the reporter.
6. Publish a security advisory and release a patched version when appropriate.

We aim to handle security reports promptly and communicate progress throughout the process.

Disclosure timelines may depend on the complexity and severity of the vulnerability and the availability of a suitable fix.

## Coordinated Disclosure

We request that reporters keep vulnerability details confidential until a fix or mitigation is available and coordinated disclosure has been agreed upon.

The maintainers will respect responsible disclosure practices and will credit reporters in security advisories when requested and appropriate.

## Security Advisories

Published security advisories and vulnerability information are available at:

https://github.com/plgd-dev/go-coap/security/advisories

## Dependencies

Security issues affecting third-party dependencies should generally be reported to the respective upstream maintainers.

However, if a vulnerability in a dependency creates a demonstrable security issue in go-coap through its integration or usage, please report it to the go-coap maintainers as described above.
