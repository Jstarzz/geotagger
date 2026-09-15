# Cloudflare setup for the GeoTagger admin control plane

This runbook finishes the edge configuration for:

```text
https://admin.geo.itsjosiahdavis.dev
```

The public machine API stays at:

```text
https://geo.itsjosiahdavis.dev
```

Do **not** put interactive Access authentication in front of the public machine API hostname.

Official references:

- Cloudflare Access self-hosted applications: <https://developers.cloudflare.com/cloudflare-one/access-controls/applications/http-apps/self-hosted-public-app/>
- Cloudflare Access JWT validation: <https://developers.cloudflare.com/cloudflare-one/access-controls/applications/http-apps/authorization-cookie/validating-json/>
- Cloudflare Access MFA: <https://developers.cloudflare.com/cloudflare-one/access-controls/policies/mfa-requirements/>
- Cloudflare Tunnel published application routes: <https://developers.cloudflare.com/tunnel/setup/>
- Cloudflare Tunnel Access origin parameter: <https://developers.cloudflare.com/tunnel/advanced/origin-parameters/#access>

## Safe rollout order

Use this order so there is no interval where an unprotected admin UI is intentionally published:

```text
1. Deploy GeoTagger code/manifests with Access settings absent
2. Create the Cloudflare Access application and restrictive policy
3. Copy the Access team domain and application AUD tag
4. Add those values to geotagger-secrets and restart API Pods
5. Add the admin published-application route to the existing GeoTagger tunnel
6. Validate edge blocking, origin JWT validation and managed-key lifecycle
```

With Access settings absent, the origin's `/admin/...` routes fail closed. `/healthz`, `/readyz` and `/metrics` remain usable inside the cluster.

## 1. Deploy the application first

Update the server to the merged release and run the normal deployment script before publishing the new hostname.

Confirm the new Service exists:

```bash
kubectl -n geotagger get svc geotagger geotagger-admin
```

Expected ports:

```text
geotagger        8080/TCP
geotagger-admin  9090/TCP
```

Before Cloudflare Access values are configured, an internal request to a privileged path should fail closed:

```bash
kubectl -n geotagger run admin-probe --rm -i --restart=Never \
  --image=curlimages/curl:latest -- \
  curl -i http://geotagger-admin.geotagger.svc.cluster.local:9090/admin/api/status
```

Expected result: `503` because the admin control plane does not yet have an Access verifier configured.

## 2. Create the Access application

In Cloudflare:

```text
Zero Trust
  -> Access controls
  -> Applications
  -> Add an application
  -> Self-hosted
```

Use:

```text
Application name: GeoTagger Admin
Hostname: admin.geo.itsjosiahdavis.dev
```

Choose a short privileged session duration. Do not use an `Everyone` allow rule.

### Allow policy

Create an `Allow` policy whose `Include` rule names only the administrator(s) or an approved identity-provider group.

For a single-administrator deployment, an exact email-address rule is preferable to allowing an entire public email domain.

### MFA

Require MFA for this application/policy.

Cloudflare Access currently supports:

- IdP-reported MFA requirements for supported identity providers; and
- independent MFA enforced directly by Access.

Prefer phishing-resistant WebAuthn/passkeys/security keys where the chosen identity provider and Access configuration support them.

Save the application and policy.

## 3. Get the Access application audience tag

Cloudflare assigns a unique Application Audience (AUD) tag to each Access application.

Current dashboard path:

```text
Zero Trust
  -> Access controls
  -> Applications
  -> GeoTagger Admin -> Configure
  -> Additional settings
  -> Application Audience (AUD) Tag
```

Copy the value exactly.

The application's Access JWT uses this value in its `aud` claim. GeoTagger validates it at the origin, so a JWT for another Access application is not accepted.

## 4. Get the team domain

GeoTagger needs the Cloudflare One team hostname used as the JWT issuer/signing-key location:

```text
<team-name>.cloudflareaccess.com
```

Do not include a path. The application constructs:

```text
https://<team-name>.cloudflareaccess.com/cdn-cgi/access/certs
```

Cloudflare documents this endpoint as the location of the Access account signing keys.

## 5. Add Access values to the Kubernetes Secret

Do not replace the existing secret with a partial manifest. Patch the two additional string values into the existing `geotagger-secrets` object.

One safe method is:

```bash
kubectl -n geotagger patch secret geotagger-secrets --type merge -p "$(cat <<'JSON'
{
  "stringData": {
    "CF_ACCESS_TEAM_DOMAIN": "YOUR-TEAM.cloudflareaccess.com",
    "CF_ACCESS_AUD": "YOUR_APPLICATION_AUD_TAG"
  }
}
JSON
)"
```

Then restart only the API Deployment:

```bash
kubectl -n geotagger rollout restart deployment/geotagger-api
kubectl -n geotagger rollout status deployment/geotagger-api --timeout=180s
```

Check readiness:

```bash
kubectl -n geotagger get pods -l app=geotagger-api
```

Both API replicas should become Ready.

## 6. Add the admin hostname to the existing Tunnel

In Cloudflare:

```text
Networking
  -> Tunnels
  -> select the GeoTagger tunnel
  -> Routes
  -> Add route
  -> Published application
```

Set:

```text
Hostname:
admin.geo.itsjosiahdavis.dev

Service URL:
http://geotagger-admin.geotagger.svc.cluster.local:9090
```

The `cloudflared` connector runs inside the same K3s namespace, so the ClusterIP DNS name is the intended origin address.

The public route is HTTPS at the Cloudflare edge while the local tunnel-to-Service hop is HTTP inside the cluster. Cloudflare Tunnel encrypts the connector path independently of the local Service URL protocol.

Save the route.

Do not point the admin hostname at the public `geo.itsjosiahdavis.dev` hostname, and do not expose NATS/ClickHouse directly.

## 7. Optional defense in depth: Tunnel Access validation

Cloudflare Tunnel also supports a `Protect with Access` / `access` origin parameter that makes `cloudflared` validate the Access JWT before proxying a protected hostname to the origin.

If your remotely-managed Tunnel UI exposes this option for the published route, enable it using:

```text
team name: <your-team-name>
aud tag: <GeoTagger Admin AUD tag>
```

GeoTagger still performs its own JWT validation at the origin. Enabling both is intentional defense in depth, not a replacement for the application check.

## 8. Edge validation

### Unauthenticated browser

Open:

```text
https://admin.geo.itsjosiahdavis.dev/admin/
```

An unauthenticated session should be intercepted by Cloudflare Access rather than showing the GeoTagger dashboard directly.

### Unauthorized identity

A user who does not match the Access allow policy must be denied.

### Authorized identity

After successful Access authentication/MFA, the dashboard should load and show:

```text
service readiness
NATS / managed-key-store state
managed-key counts
active MMDB version
signed-in administrator email
```

## 9. Prove origin validation

Cloudflare documents that an Access-protected request forwarded to the origin contains:

```text
Cf-Access-Jwt-Assertion: <signed application JWT>
```

GeoTagger independently validates that assertion. An internal request that bypasses Cloudflare and omits the header must return `401` once Access settings are enabled:

```bash
kubectl -n geotagger run admin-probe --rm -i --restart=Never \
  --image=curlimages/curl:latest -- \
  curl -i http://geotagger-admin.geotagger.svc.cluster.local:9090/admin/api/status
```

Expected:

```text
HTTP 401
```

This proves the origin is not trusting the Tunnel hostname alone.

## 10. End-to-end managed-key acceptance test

From the dashboard:

1. Create a disposable key such as `admin-smoke-test`.
2. Copy the one-time token immediately.
3. Use the token against the normal machine API:

```bash
curl -i \
  -H 'Authorization: Bearer admin-smoke-test.REPLACE_WITH_ONE_TIME_SECRET' \
  'https://geo.itsjosiahdavis.dev/v1/lookup?ip=8.8.8.8'
```

Expected: successful full lookup.

4. Rotate the key.
5. Confirm the old token returns `401` and the replacement token succeeds.
6. Revoke the replacement.
7. Confirm it now returns `401`.
8. Confirm the existing static production/break-glass token still authenticates.

## 11. Cross-replica propagation check

The Deployment normally starts with two API Pods. Managed-key changes should propagate through NATS KV without restarting the Pods.

After create/rotate/revoke:

```bash
kubectl -n geotagger get pods -l app=geotagger-api -o wide
kubectl -n geotagger get pods -l app=geotagger-api \
  -o 'custom-columns=NAME:.metadata.name,READY:.status.containerStatuses[0].ready,IMAGE_ID:.status.containerStatuses[0].imageID'
```

Both replicas should remain Ready.

A fresh managed token should continue working across repeated public requests; a revoked token should consistently fail.

## Rollback / emergency disable

To disable the browser admin surface without affecting the public machine API:

1. disable/delete the `admin.geo.itsjosiahdavis.dev` Tunnel route **or** disable the Access application;
2. optionally remove `CF_ACCESS_TEAM_DOMAIN` and `CF_ACCESS_AUD` from the Kubernetes Secret together; and
3. restart `deployment/geotagger-api`.

Do not delete the NATS KV bucket simply to disable the UI; it contains managed credential state. If managed credentials themselves must be invalidated, revoke them through the control plane before disabling it or perform a deliberate documented recovery procedure.

## Final acceptance checklist

```text
[ ] Access app is Self-hosted and targets only admin.geo.itsjosiahdavis.dev
[ ] allow policy is restricted to named administrator(s)/approved group
[ ] MFA is required
[ ] no Everyone allow policy exists
[ ] CF_ACCESS_TEAM_DOMAIN matches the Access issuer team hostname
[ ] CF_ACCESS_AUD matches the GeoTagger Admin application AUD tag
[ ] admin Tunnel route targets geotagger-admin:9090
[ ] unauthenticated browser is intercepted by Access
[ ] unauthorized user is denied
[ ] authorized admin reaches the dashboard
[ ] direct internal request without Access JWT returns 401
[ ] create/rotate/revoke lifecycle passes
[ ] both API replicas remain Ready and converge without restart
[ ] old public/static API client still works
[ ] public geo hostname has no interactive Access requirement
```
