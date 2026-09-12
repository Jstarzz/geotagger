# MFA and Identity Policy

## Decision

GeoTagger's public lookup API is a **machine-to-machine API**. Interactive MFA is therefore not performed on every `/v1/country` request. Machine callers authenticate with a unique high-entropy credential and can be upgraded to stronger machine identity such as mTLS or a signed workload identity if the deployment requires it.

**Human privileged access should use MFA.** This applies to the systems from which an administrator can change GeoTagger, its secrets, traffic path, backups, or audit data.

Minimum administrative MFA scope:

```text
Cloudflare account / tunnel administration
GitHub repository and CI administration
Proxmox administration
SSH / bastion / VPN used for server administration
Kubernetes administrative access
secret-management / backup systems
identity provider and recovery accounts
any future GeoTagger human admin UI
```

Where a platform supports phishing-resistant MFA such as WebAuthn/FIDO2/passkeys or hardware security keys, prefer it over SMS or email OTP for privileged administration.

## HIPAA status as of September 2026

The HHS page describing the **currently effective HIPAA Security Rule** continues to require regulated entities to implement reasonable and appropriate administrative, physical and technical safeguards, including procedures to verify that a person or entity seeking access to ePHI is who they claim to be.

Current-rule reference:

https://www.hhs.gov/hipaa/for-professionals/security/laws-regulations/index.html

HHS also published a proposed modernization of the Security Rule in January 2025. The proposal would require multi-factor authentication with limited exceptions, along with other stronger cybersecurity requirements.

Proposed-rule reference:

https://www.hhs.gov/hipaa/for-professionals/security/hipaa-security-rule-nprm/factsheet/index.html

The HHS Security Rule history page still identifies this modernization as a **proposed rule**, not the currently effective final rule:

https://www.hhs.gov/hipaa/for-professionals/security/index.html

Therefore the repository does not state that universal MFA is already an explicit final-rule requirement. GeoTagger instead adopts MFA for human privileged access as a prudent security baseline and to avoid designing around a control that HHS has already proposed making mandatory.

HHS/OCR's January 2026 cybersecurity guidance also gives MFA as an example of a control that a regulated entity's risk analysis may determine is necessary to reduce unauthorized-access risk:

https://www.hhs.gov/hipaa/for-professionals/security/guidance/cybersecurity-newsletter-january-2026/index.html

## Why MFA does not belong in the API request itself

The lookup API is called by software, not a human sitting at an MFA prompt.

A request path such as:

```text
NDHIS service
  -> bearer token
  -> GeoTagger
```

should not become:

```text
NDHIS service
  -> ask a human for OTP every request
  -> GeoTagger
```

That would break automation and does not represent appropriate machine authentication.

For machine callers, stronger authentication means controls such as:

- unique secret per calling service;
- high-entropy secrets;
- short/defined rotation period;
- immediate revocation capability;
- source/network restrictions where appropriate;
- mTLS client certificates;
- signed workload identity tokens with issuer/audience/expiry checks; or
- an API-gateway/service-token mechanism approved for the environment.

## Recommended machine-authentication roadmap

### Current

```text
caller ID + random bearer secret
server stores SHA-256 digest only
constant-time verification
```

This is acceptable for the current controlled service when secrets are distributed and stored correctly.

### Stronger production option

For a higher-assurance ePHI environment, use two independent machine controls where practical:

```text
mTLS or trusted workload identity
        +
per-client application credential / authorization
```

This is the machine equivalent of stronger multi-factor assurance without forcing an interactive human flow.

Do not add a second factor solely to satisfy a checklist if both factors are stored in the same plaintext `.env` on the same compromised client host; that provides little independent protection.

## Administrative-account requirements

For every privileged human account:

```text
[ ] unique named account; no shared administrator login
[ ] MFA enabled
[ ] phishing-resistant MFA preferred where supported
[ ] recovery methods reviewed and protected
[ ] least-privilege role assigned
[ ] access removed promptly when no longer required
[ ] access reviewed periodically
[ ] administrative actions logged where the platform supports it
[ ] break-glass account documented, protected and monitored
```

Shared root credentials should not be the normal administrative identity model. If root access is required on Linux, authenticate as an individually attributable administrator and elevate through a controlled mechanism where the environment supports it.

## Break-glass access

A production healthcare environment may require emergency administrative access. The break-glass path should not silently disable all security controls.

Document:

- who can activate it;
- where credentials/factors are stored;
- how access is logged;
- when credentials are rotated after use;
- who reviews each activation; and
- how normal controls are restored.

## Service-account lifecycle

Every GeoTagger API credential should have an external inventory record containing:

```text
key ID
owning system/team
purpose
environment
issue date
rotation/expiry date
last rotation date
revocation status
incident/contact owner
```

The API itself currently stores the allowed key IDs and digests, not full lifecycle metadata. The operational inventory closes that gap until a dedicated credential-management system is introduced.

## What not to do

Do not:

- put a shared browser login challenge in front of the entire hostname if machine clients cannot satisfy it;
- reuse one API token across unrelated systems;
- compile machine secrets into client-side JavaScript;
- store production tokens in Git;
- treat IP allowlisting as a replacement for authentication;
- treat Cloudflare being in front of the service as proof that the caller is authorized; or
- describe machine bearer auth as 'MFA' merely because the connection also uses TLS.

## Production gate

Before ePHI use:

```text
[ ] privileged human platforms use MFA where supported
[ ] organization risk analysis has evaluated authentication requirements
[ ] all callers have unique service credentials
[ ] credential rotation and revocation procedures are tested
[ ] administrator identities are individually attributable
[ ] recovery and break-glass procedures are documented
[ ] upstream human identity, when relevant, comes from an authoritative identity provider
[ ] machine-identity strengthening such as mTLS has been considered and dispositioned
```

This file is an engineering policy. The covered entity/business associate remains responsible for the final risk-analysis and compliance determination.
