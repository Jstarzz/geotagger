# HIPAA Readiness and Control Matrix

## Status

GeoTagger is **not certified or declared HIPAA compliant by this repository**. HIPAA compliance is an organizational and operational state, not a property that can be created by application code or documentation alone.

This document defines the controls that must be in place before GeoTagger is permitted to create, receive, maintain, or transmit electronic protected health information (ePHI). It also records which safeguards are implemented in the current system and which require action by the operator or covered entity/business associate.

The authoritative requirements are the HIPAA Rules and current HHS/OCR guidance. Relevant HHS references include:

- Security Rule summary: https://www.hhs.gov/hipaa/for-professionals/security/laws-regulations/index.html
- Risk analysis guidance: https://www.hhs.gov/hipaa/for-professionals/security/guidance/guidance-risk-analysis/index.html
- Cloud computing guidance: https://www.hhs.gov/hipaa/for-professionals/special-topics/health-information-technology/cloud-computing/index.html
- Business associate contract guidance: https://www.hhs.gov/hipaa/for-professionals/covered-entities/sample-business-associate-agreement-provisions/index.html
- Breach Notification Rule: https://www.hhs.gov/hipaa/for-professionals/breach-notification/index.html

If this document conflicts with law, regulation, a BAA, or current HHS/OCR guidance, the governing requirement takes precedence.

## When HIPAA may apply

An IP address processed in isolation is not automatically ePHI. HIPAA applicability depends on the data context and the parties involved. If GeoTagger is used by a covered entity or business associate and the request or audit data can be linked to an individual and health information, the data path may fall within the ePHI boundary.

The safest deployment rule is:

> If GeoTagger participates in a workflow containing ePHI, treat the service, logs, backups, infrastructure, operators, and vendors in that data path as part of the HIPAA security boundary until the organization's privacy/security officer determines otherwise.

## Current data-minimization posture

The default audit mode is `hmac`.

```text
Queried public IP
        |
        v
HMAC-SHA256 with secret key
        |
        v
Audit value stored in ClickHouse
```

The raw queried IP is not retained in the default mode. Authentication failures are audited without retaining the requested target IP. This reduces retained identifier exposure, but it does not by itself remove the service from HIPAA scope when the service participates in an ePHI workflow.

## Security Rule control matrix

The table below is an engineering mapping, not a legal determination.

| Safeguard area | Current technical control | Remaining organizational/technical work |
|---|---|---|
| Security management process / risk analysis | Security audit, documented trust boundaries, bounded request path | Covered entity/business associate must perform and document an accurate and thorough risk analysis and risk-management plan for the real environment |
| Assigned security responsibility | Repository defines system components and operational roles | Organization must name the responsible security official and define ownership/escalation |
| Workforce security | Not implemented in GeoTagger; API uses machine credentials | Workforce authorization, termination, access review, training, sanctions and administrative procedures must exist outside the service |
| Information access management | Per-caller API keys; internal services are network-restricted | Caller issuance must be tied to approved systems/owners; upstream systems must provide unique user authorization where workforce users initiate requests |
| Security awareness and training | Not an application control | Organization must operate a training and security-awareness program |
| Security incident procedures | Request IDs, audit events, runbook documentation | Organization must establish incident classification, escalation, investigation, evidence preservation, breach assessment and notification procedures |
| Contingency plan | Durable NATS queue; persistent ClickHouse storage | Tested backups, disaster recovery, emergency-mode operations, RPO/RTO and restore evidence are required |
| Evaluation | CI, security checklist, load testing | Periodic technical and nontechnical evaluation must be scheduled and documented after material environmental/operational changes |
| Business associate arrangements | No assumption that vendor contracts exist | Execute required BAAs with vendors/subcontractors that create, receive, maintain or transmit ePHI on behalf of the regulated entity |
| Facility access controls | On-prem Proxmox host reduces external physical surface | Physical server/network/storage access must be restricted, documented and reviewed by the operator |
| Workstation/device controls | Service is server-side | Administrative workstation and removable-media controls remain organizational responsibilities |
| Access control | Machine bearer authentication; Kubernetes RBAC boundary; internal services not public | Production credential lifecycle, least privilege, emergency-access procedures where applicable, and unique workforce attribution must be defined |
| Audit controls | Durable request audit, `X-Request-ID`, ClickHouse audit records | Define protected retention, review cadence, alerting, administrator-access audit, export/archival and tamper-resistance requirements |
| Integrity | HMAC IP representation; durable queue; application validation | Backup integrity checks, deployment provenance and documented integrity-verification procedures must be operated |
| Person/entity authentication | Machine caller secret is verified | If individual workforce identity matters, authentication must occur in an authoritative upstream identity system and be propagated as trusted context, not self-asserted metadata |
| Transmission security | Public traffic uses HTTPS through Cloudflare Tunnel; origin is not directly exposed | Contract/vendor review, BAA where required, TLS policy verification and approved client authentication must be documented |
| Policies/procedures/documentation | Repository contains architecture/security/runbooks | HIPAA-required policies, decisions, assessments and procedures must be retained according to the organization's documentation-retention obligations |

## Required production gate before ePHI

Do not intentionally place ePHI in the GeoTagger workflow until every applicable item below has an accountable owner and evidence.

```text
[ ] HIPAA applicability and data-flow determination approved by privacy/security leadership
[ ] Formal risk analysis completed for all ePHI created, received, maintained or transmitted
[ ] Risk-management plan approved; identified risks have owners and due dates
[ ] Required BAAs executed for all applicable vendors/subcontractors
[ ] Cloudflare service/tier and contract approved for the intended ePHI use
[ ] Public TLS and origin/tunnel configuration reviewed
[ ] Production API credentials uniquely assigned per calling system
[ ] Credential issue, rotation, revocation and compromise procedures documented
[ ] Upstream workforce identity and authorization are authoritative where user attribution is required
[ ] Kubernetes/Proxmox administrative access follows least privilege and MFA where supported
[ ] Kubernetes Secrets encryption is verified on the running cluster
[ ] Backup schedule implemented to storage independent of the VM failure domain
[ ] Restore procedure tested and evidence retained
[ ] NATS and ClickHouse recovery procedures tested
[ ] RPO and RTO approved by the service owner
[ ] Audit-retention and review requirements approved
[ ] Security event alerting/escalation configured
[ ] Incident-response and breach-assessment procedure approved and exercised
[ ] Vulnerability/patch-management cadence defined
[ ] Production container images pinned to approved immutable references
[ ] Egress policy reviewed and enabled after staging validation
[ ] Physical server/storage/network access controls documented
[ ] Change-management and rollback procedures approved
[ ] Documentation retention responsibilities assigned
[ ] Periodic security evaluation/review schedule established
```

## Business associate and cloud-service boundary

HHS guidance states that when a cloud service provider creates, receives, maintains, or transmits ePHI on behalf of a covered entity or business associate, an appropriate business associate agreement is required unless a specific legal exception applies.

For GeoTagger, review at least these parties when ePHI is in scope:

```text
NDHIS / covered entity or business associate
        |
        +-- GeoTagger operator
        |
        +-- Cloudflare, if ePHI traverses the service
        |
        +-- backup/storage providers, if they receive or maintain ePHI
        |
        `-- any support/monitoring provider with access to ePHI
```

A technical statement such as "Cloudflare Tunnel encrypts traffic" is not a substitute for required contractual arrangements.

## Identity boundary

GeoTagger authenticates machine callers, not individual clinicians or patients. A caller token identifies the integrating system, for example `azure-prod`.

If an upstream NDHIS workflow requires individual accountability, the upstream system must maintain authoritative user identity and authorization. If that identity must appear in GeoTagger audit records, add it only through a trusted, authenticated integration design. Do not accept arbitrary client-supplied user names as audit identity.

## Audit requirements

The current audit pipeline records technical request events with durable queue acceptance and asynchronous ClickHouse persistence.

For a regulated deployment, define in writing:

- which events must be recorded;
- who may read or administer audit data;
- how administrator access to the platform is audited;
- retention duration;
- archival/export requirements;
- review frequency;
- alert conditions;
- clock synchronization requirements;
- evidence-preservation procedure; and
- deletion/destruction procedure after the approved retention period.

The existing 30-day ClickHouse TTL is an application default, not a universal HIPAA retention requirement. The organization must set retention based on its legal, contractual, operational and documentation requirements.

## Backup and contingency requirements

The current single-node architecture does not provide infrastructure high availability. Before ePHI use, implement and test independent backups. See `BACKUP_RECOVERY.md`.

At minimum, preserve:

- ClickHouse audit data required by policy;
- NATS JetStream state when needed for recovery;
- Kubernetes manifests and approved deployment versions;
- encrypted secret-recovery material under controlled access;
- K3s/cluster configuration;
- MMDB/update configuration; and
- documented restore procedures.

Backups must not live only on the same VM/data disk they are intended to recover.

## Breach and incident handling

A security incident is not automatically a reportable HIPAA breach, and breach determination is not made by this software. The operator must have a process to identify, contain, investigate and escalate incidents for privacy/security review. See `INCIDENT_RESPONSE.md`.

When unsecured PHI may have been impermissibly used or disclosed, follow the organization's breach-assessment process and the applicable HIPAA Breach Notification Rule requirements.

## Documentation retention

HHS states that documentation required by the Security Rule must be maintained for the required period under 45 CFR 164.316. The organization should retain applicable policies, procedures, risk analyses, evaluations, approvals, training/incident evidence and control decisions in its governed compliance repository. Git history alone should not be treated as the compliance record system.

## Approval record

Before ePHI use, record an approval containing at least:

```text
system: GeoTagger
version/commit:
environment:
data classification:
covered entity/business associate role:
risk analysis reference:
BAA/vendor review references:
RPO:
RTO:
audit retention:
security owner:
privacy owner:
operations owner:
approval date:
next review date:
```

Until that approval exists, the correct description is **HIPAA-readiness work in progress**, not "HIPAA compliant."
