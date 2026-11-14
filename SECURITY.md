# Security Policy

The security of Tash and its users is important.

This document explains which versions receive security updates and how to report potential security vulnerabilities.

## Supported Versions

Security fixes are prioritized for the latest stable release.

| Version | Supported |
|---|---|
| Latest stable release | Yes |
| Older releases | No |
| Development builds | Best effort |

Users are encouraged to update to the latest stable version.

## Reporting a Vulnerability

If you discover a potential security vulnerability in Tash, please report it responsibly.

**Do not publicly disclose vulnerabilities before they have been reviewed.**

When reporting a vulnerability, include:

- A clear description of the issue.
- The affected Tash version.
- Your operating system and architecture.
- Steps to reproduce the issue, if safe to do so.
- Relevant logs or error messages.
- The potential security impact.

Do not include passwords, API keys, Tashnet join keys, or other private credentials in reports.

### Where to Report

Use GitHub's private vulnerability reporting feature if it is enabled for the repository.

Repository:

https://github.com/nypryt/Tash

If private vulnerability reporting is unavailable, use a private contact method published by the repository maintainer.

Avoid posting exploitable vulnerability details in public issues.

## Security Considerations

Tash integrates with external services and system-level tools. Users should understand the following risks.

### Docker

Docker integration can provide access to container management operations.

Depending on the configuration and permissions, these operations may affect running services and system resources.

Only enable Docker access when needed.

### Tailscale

Tailscale integration provides access to devices on your configured network.

Only connect to devices and networks you trust.

### Pi-hole

Tash can communicate with Pi-hole using configured authentication credentials.

Protect these credentials and avoid sharing configuration files that contain them.

### Tashnet

Tashnet uses join keys and device approval to manage network membership.

- Keep join keys private.
- Approve only trusted devices.
- Protect temporary main-transfer codes.
- Avoid sharing secret configuration files.

### NAS

Tash includes a WebDAV server using Basic Authentication over HTTP.

- Use trusted networks.
- Avoid exposing the service directly to the public internet.
- Use Tailscale or another appropriate private network for remote access.
- Protect the configured NAS password.

### AI Integrations

AI integrations may access files and execute tools depending on their configuration.

- Review permission settings.
- Check the configured working directory.
- Avoid providing unnecessary secrets to AI providers.
- Review commands before allowing execution.

## Security Limitations

Tash is developed with AI-assisted development tools and may contain bugs, vulnerabilities, or incomplete functionality.

The project has not necessarily undergone a comprehensive professional security audit.

No guarantee is made that every component or integration is free from security vulnerabilities.

Users should evaluate their own security requirements before deploying Tash in sensitive environments.

## Security Updates

Security-related fixes and improvements may be included in future releases.

Check the official repository for updates:

https://github.com/nypryt/Tash

---

Copyright © 2026 Nypr. All rights reserved.

Developed by **Nypr**, under **Nypr Studioz**.
