# JetStream structural-contract correction

Follow-up to the durable JetStream contract hardening.

NATS does not support changing an existing stream retention policy to or from WorkQueue in place. GeoTagger therefore treats the audit stream's storage backend and retention policy as structural invariants:

- `FileStorage` is required;
- `WorkQueuePolicy` is required;
- an incompatible existing stream causes startup to fail with an operator-actionable error;
- only mutable safety settings are automatically reconciled.

This avoids pretending an incompatible stream can be repaired safely at API startup and prevents a destructive migration from happening implicitly.

Integration coverage distinguishes:

1. stale but mutable settings, which are repaired automatically;
2. memory-backed storage, which is rejected; and
3. non-WorkQueue retention, which is rejected because NATS cannot migrate it in place.
