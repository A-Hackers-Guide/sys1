# Security policy

## Reporting a vulnerability

Please report vulnerabilities **privately** through GitHub's private vulnerability reporting:

**[Report a vulnerability](https://github.com/A-Hackers-Guide/sys1/security/advisories/new)**

Include what you found, how to reproduce it, and what an attacker could do with it. We'll acknowledge it as soon as we can, work on a fix with you, and credit you in the advisory unless you prefer otherwise.

Please don't open a public issue or disclose it publicly until a fix is released.

## Supported versions

sys1 is pre-1.0. Security fixes go into the latest release only.

## Scope

In scope: sys0, sys1, the dashboard, the setup wizard, the protocol, builtin marketplace packages, the release pipeline, and the install scripts.

Out of scope: vulnerabilities in Docker/Podman, in the OS, or in third-party container images (report those upstream), and issues that need an attacker who already controls the node's dashboard key or the machine itself.

How sys1 protects people is documented in [docs/SECURITY-MODEL.md](docs/SECURITY-MODEL.md), including known open problems.
