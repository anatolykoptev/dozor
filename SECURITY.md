# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in Dozor, please report it responsibly.

**Email**: [anatoly@koptev.me](mailto:anatoly@koptev.me)

Please include:
- Description of the vulnerability
- Steps to reproduce
- Potential impact

I will acknowledge your report within 48 hours and aim to release a fix within 7 days for critical issues.

## Scope

Dozor executes shell commands on the host system by design. Security concerns include:
- Command injection bypassing the blocklist
- Path traversal in service names or deploy IDs
- Information disclosure through error messages

## Supported Versions

| Version | Supported |
|---------|-----------|
| latest  | Yes       |
