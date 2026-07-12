# Security Policy

Thank you for helping keep Phlox-GW and its users secure. Please report suspected
vulnerabilities privately so they can be investigated and fixed before public
disclosure.

## Supported Versions

Phlox-GW is currently a pre-1.0 project. Security fixes are provided for the latest
stable release only. Users may need to upgrade to the latest release to receive a fix.

| Version | Supported |
| --- | --- |
| Latest stable release | Yes |
| Older releases and prereleases | No |

The latest stable version is listed on the
[GitHub Releases page](https://github.com/robert-mcdermott/phlox-gw/releases/latest).
This policy will be revisited as the project and its release channels mature.

## Reporting a Vulnerability

Do not report suspected vulnerabilities in a public GitHub issue, discussion, pull
request, or other public forum.

Use GitHub's private vulnerability reporting form:

**[Report a vulnerability privately](https://github.com/robert-mcdermott/phlox-gw/security/advisories/new)**

If that form is unavailable, use the repository's
[Security page](https://github.com/robert-mcdermott/phlox-gw/security) to check for the
current private reporting method. Please do not publish vulnerability details while
seeking an alternate contact route.

Include as much of the following as possible:

- The affected Phlox-GW version, commit, and deployment configuration.
- The vulnerability type and affected component or endpoint.
- Clear reproduction steps or a minimal proof of concept.
- The security impact and the conditions required to exploit it.
- Any known mitigations or suggested fixes.
- Whether the issue is already public or has been shared with anyone else.
- How you would like to be credited in an advisory, if at all.

Do not include real API keys, credentials, personal data, or other third-party secrets.
Use synthetic test data and redact sensitive logs before attaching them.

## What to Expect

The following are response targets, not a service-level agreement:

- Acknowledgement within 5 business days.
- An initial assessment within 10 business days when sufficient reproduction details
  are available.
- Progress updates at least every 10 business days while an accepted report remains
  unresolved.

After triage, the maintainer will confirm whether the report is accepted, request any
needed details, and coordinate remediation and disclosure. Resolution time depends on
severity, complexity, and release risk. When appropriate, a fix will be released with a
GitHub Security Advisory and CVE before or alongside coordinated public disclosure.

Please allow a reasonable remediation window and coordinate any public disclosure with
the maintainer. Reports that are duplicates, not reproducible, or outside the supported
scope may be closed with an explanation.

## Scope

This policy covers vulnerabilities in:

- The Phlox-GW source code and latest stable release binaries.
- The official `install.sh` installer and release checksum workflow.
- Authentication, authorization, secret handling, gateway behavior, and the embedded
  administrative web interface.

Issues in third-party providers, identity platforms, model services, operating systems,
or dependencies should normally be reported to the responsible project or vendor. A
dependency issue is in scope when Phlox-GW uses it in an exploitable way or needs a
project-level mitigation.

General bugs, feature requests, and hardening suggestions without a concrete security
impact should be filed as regular GitHub issues.

## Good-Faith Research

Please make a good-faith effort to avoid privacy violations, data destruction, service
disruption, social engineering, and access to data beyond what is necessary to
demonstrate the issue. Test only systems and data you own or have explicit permission to
use.

The project does not currently operate a bug-bounty program and cannot offer payment for
reports. Good-faith reports may be credited in the advisory and release notes with the
reporter's permission.
