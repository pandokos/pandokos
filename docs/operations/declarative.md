# Declarative configuration guide

`pandokos export` writes the configuration of the platform or of one tenant as a YAML document, and
`pandokos apply` brings an installation in line with a document, through the administration API
([ADR 0066](../adr/0066-declarative-export-and-apply.md), [ADR 0019](../adr/0019-configuration.md)).
The document can be reviewed, kept in version control and repeated; the database is not a second source
of truth, because every change goes through the same operations, permissions and audit rows as a manual
one. Both commands authenticate as every API command does ([admin API](../reference/admin-api.md)).

```sh
pandokos export --tenant demo --out demo.yaml        # or --platform; without --out, standard output
pandokos apply --tenant demo --file demo.yaml --dry-run
pandokos apply --tenant demo --file demo.yaml
```

## 1. The document

A document is one YAML file of at most 4 MiB, with no anchors or aliases and no unknown member. It starts
with `pandokos: v1` and a `kind`, `platform` or `tenant`. Resources are named by their **natural keys**;
the members of an item are those of the create body of the API, minus what the server sets (IDs, times,
computed states). Rates and amounts are decimal strings and are never converted.

| Kind | Section | Natural key | Members |
|---|---|---|---|
| `platform` | `providers` | `name` | `api_family`, `display_name`, `description`, `dataset_provider`, `stream_usage_option`, `endpoints` |
| | `providers[].endpoints` | `name` within the provider | `base_url`, `region`, `private`, `is_default`, `description` |
| | `models` | `provider`, `name` | `upstream_id`, `display_name`, `description`, `status`, `availability`, `capabilities`, `context_window`, `max_output_tokens`, `default_output_tokens` |
| | `model_grants` | `provider`, `model`, `tenant` (the tenant's **name**) | none |
| | `price_overrides` | `provider`, `model`, `valid_from` | `note`, `currency`, `rates` |
| | `platform_limits` | `tenant` (a name, or `default`), `provider`, `model`, `metric`, `currency` | `limit` or `amount`, `enforce`, `thresholds` |
| | `platform_issuers` | `name` | `description`, `issuer`, `audience`, `jwks_uri`, `algorithms`, `subject_claim`, `tenant_claim`, `required_claims`, `enabled`, `tenants` (each `tenant`, a name, and `claim_value`) |
| | `tenants` | `name` | `external_reference`. This section **never creates a tenant**: a name that does not exist is a `conflict`, and the reference is the one member the platform sets |
| `tenant` | `settings` | none | `time_zone`, `active_currency`, `metadata_keys` |
| | `model_restriction` | none | `models` (`provider/model` references) |
| | `roles` | `name` (custom roles only) | `description`, `permissions` |
| | `service_accounts` | `name` | `description`, `valid_from`, `valid_until`, `roles` |
| | `limits` | `subject`, `provider`, `model`, `metric`, `currency` | `limit` or `amount`, `enforce`, `thresholds` |
| | `price_lists` | `currency` | `versions` |
| | `price_lists[].versions` | `provider`, `model`, `valid_from` | `note`, `rates` (`category`, `threshold`, `rate`) |
| | `token_issuers` | `name` | `description`, `issuer`, `audience`, `jwks_uri`, `algorithms`, `subject_claim`, `required_claims`, `enabled` |
| | `subject_mappings` | `issuer` (`scope`, `name`), `subject` | `service_account` (a name), `description` |
| | `event_destinations` | `name` | `description`, `url`, `event_types`, `enabled` |

In `limits` the `subject` is `tenant`, `service_account: <name>`, `key: <key ID>` (the public key ID) or a
subject mapping, `mapping: {scope: platform, issuer: <name>, subject: <subject>}`; a rule on a mapping is
written after the others in the export, ordered by the text of its subject. A claim value that equals the
tenant's ID is not exported, and a platform issuer's tenant with no `claim_value` means the ID of the target
tenant, because IDs are not portable between installations.
A document may hold only the sections it cares about; a section that is absent means "leave alone", not
"remove".

**What is never in a document, and never exported:** a secret, a key, a credential, a person (member,
user, session), a usage figure, an ID that is not portable. The signing secret of a destination is one of
those: a document that holds a `secret` or a `signing_secret` is refused (exit 2). Platform-level issuers with
their tenants, tenant token issuers, subject mappings, destinations and the external reference of a tenant are
in the document; members, credentials and keys never will be. `apply` never creates a tenant, never deletes and
never rotates a secret.

**Destinations and `--secrets-out`.** A destination has a signing secret that is shown once, when it is created.
`apply` therefore creates a destination only when it is given `--secrets-out DIR`: the secret is written to a
file named `event-destination-<name>` in that directory (which must be private; each file is created exclusively
with mode 0600) and is never printed. Without the flag `apply` skips the destination and says so, and a
destination that exists is updated as any resource. Move the files to your secret store and delete them: nothing
else holds the secret, and `pandokos event-destination rotate-secret` replaces it.

**Settings the document cannot clear.** An absent or empty `time_zone` leaves the setting as it is,
and so does an absent `metadata_keys`; a document cannot return a tenant to "every metadata key" or to
the installation's time zone. Use `pandokos metadata-keys clear` for that. An explicit empty list,
`metadata_keys: []`, is different: it sets the empty policy, a tenant that accepts no caller metadata
key at all, and the export of such a tenant says so.

## 2. `export`

`pandokos export (--platform | --tenant ID) [--out FILE]` reads through the API and writes the document
deterministically: sections and items sorted by natural key, so two exports of one state are identical.
With `--out` it writes `FILE.partial` and renames it at the end. For a platform document the model grants
and price overrides are read only for the models that can have them. Export needs the read permissions
of what it reads; a missing one is the API's `403`.

## 3. `apply` and its rules

`pandokos apply --file FILE (--platform | --tenant ID) [--dry-run] [--secrets-out DIR]` reads the current state through the
API, compares it with the document by natural key and plans:

| Plan | When |
|---|---|
| `create` | the resource is absent |
| `update` | a mutable member differs (the update operation of that resource) |
| `conflict` | an immutable member differs (for example the `api_family` of a provider), or something the item refers to does not exist (a tenant name in a grant, a service account in a limit's subject): reported, nothing is changed |
| `skipped` | a price version whose `valid_from` is in the past and which does not exist |
| `not in document` | the resource exists but the document does not name it: **left alone and reported**; nothing is ever deleted and there is no `--prune`. Removing a resource stays an explicit API call |

- **Versions are only added.** A price version (a platform override or a tenant list's) whose
  `(provider, model, valid_from)` exists is unchanged when its rates are equal and a `conflict`
  otherwise. An absent version is created when its `valid_from` is now or later, and reported
  `skipped: in the past` otherwise: `apply` never creates a price version dated before now, because the
  database refuses one and the history of what was charged cannot be rewritten.
- **Settings that take effect later** (the time zone, the active currency) compare the value set,
  pending included, so a document just exported compares equal.
- **Order.** Platform: providers, endpoints, models, grants, overrides, ceilings, the external references of tenants,
  then the platform issuers with their tenants. Tenant: time zone and metadata keys, custom roles, service accounts
  and their roles, model restriction, token issuers, subject mappings, limits, price lists, the active currency
  (a budget may need its price list first), and the destinations last.
- **Updates.** An **absent** member leaves the stored value unchanged; an **explicitly empty** one clears it. This
  holds for `enabled`, `jwks_uri`, `required_claims` (`required_claims: {}`) and `event_types` (`event_types: []`)
  of an issuer or a destination, and an empty `description` leaves the stored one. `algorithms`, `subject_claim`
  and `tenant_claim` are compared only when written. On a **create**, an absent `enabled` creates the item
  **disabled** (the safe default) and absent `required_claims` and `event_types` create it empty; an export always
  writes all of these members, so a document just exported changes nothing and is safe to edit.
- **Idempotent.** The plan of a document just exported is empty: no write call, `0 changes`.

**Output and exit codes.** One line per change, such as `create service_account billing`,
`update limit …`, `conflict …`, `skipped …`, `not in document …`, then
`<n> changes, <m> conflicts, <k> left alone`. With `--dry-run` the same lines are printed and nothing is
written.

| Exit | Meaning |
|---|---|
| 0 | every planned change succeeded (conflicts and left-alone resources are reported, not failures) |
| 1 | a call failed; the rest is not attempted, and running the command again converges |
| 2 | a usage error or an invalid document (unknown member, wrong value, a forbidden member such as `secret`) |

## 4. Examples

A platform document:

```yaml
pandokos: v1
kind: platform
providers:
  - name: openai
    api_family: openai
    endpoints:
      - name: default
        base_url: https://api.openai.com
        is_default: true
models:
  - provider: openai
    name: gpt-4o
    upstream_id: gpt-4o-2024-11-20
    status: active
    availability: selected
    capabilities: [text, vision, tools, streaming]
model_grants:
  - provider: openai
    model: gpt-4o
    tenant: acme
price_overrides:
  - provider: openai
    model: gpt-4o
    valid_from: "2027-01-01T00:00:00Z"
    currency: USD
    rates:
      - {category: input, threshold: 0, rate: "2.00"}
      - {category: output, threshold: 0, rate: "8.00"}
platform_limits:
  - tenant: default
    metric: requests_per_minute
    limit: 600
    enforce: true
```

A tenant document:

```yaml
pandokos: v1
kind: tenant
settings:
  time_zone: Europe/Rome
  active_currency: EUR
  metadata_keys: [project, team]
roles:
  - name: billing-readers
    permissions: [limit:read, usage:read]
service_accounts:
  - name: billing
    roles: [usage-viewer]
limits:
  - subject: tenant
    metric: requests_per_day
    limit: 1000
    enforce: true
    thresholds: [80, 100]
price_lists:
  - currency: EUR
    versions:
      - provider: openai
        model: gpt-4o
        valid_from: "2027-01-01T00:00:00Z"
        rates:
          - {category: input, threshold: 0, rate: "2.50"}
          - {category: output, threshold: 0, rate: "10.00"}
```

A dry run against a tenant that has none of it prints one `create` line per item and
`N changes, 0 conflicts, 0 left alone`; run again after the real `apply`, it prints `0 changes`.
