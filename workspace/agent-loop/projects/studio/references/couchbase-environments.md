# Studio Couchbase environments

Use this contract for Studio database operations and staging deployment
configuration. Check the selected environment's current deployment variables and
runtime configuration before connecting; historical reports and inherited shell
values are not environment selectors. Keep passwords in the approved local
secret store or deployment Secret, never in this package, command output or chat.

## Data-source boundary

| Data source | Cluster | Bucket / scope | Credential role |
| --- | --- | --- | --- |
| Studio staging | `couchbase-cn-2` | `voice-agent-studio` / `staging` | Dedicated runtime user `voice-agent-studio-staging` |
| Studio production | Existing `couchbase-cn` | `voice-agent-studio` / `_default` | Existing production deployment credentials; a prepared new-cluster account does not mean production has migrated |
| ConvoAI RTSC / PipelineHistory | Existing ConvoAI data source | Its separately configured bucket / scope | Its own credentials; do not replace with Studio staging credentials |

The staging cutover keeps the existing old-to-new XDCR configuration. Do not
require changing or deleting the shared replication as a prerequisite for normal
staging work. Production migration and replication configuration changes require
their own authorization. Old staging application writers stay stopped after
cutover; the old source database can remain running. XDCR is one-way, so switching
an application back does not synchronize writes made on the new cluster: reconcile
those writes before a database rollback.

## Staging connection settings

Use the complete configuration tuple; changing only a bootstrap IP is insufficient.

```dotenv
CB_QUERY_ENDPOINTS=http://49-232-236-50.couchbase.agora.io:8093,http://49-233-32-129.couchbase.agora.io:8093
CB_KV_CONNECTION_STRING=couchbase://49-233-208-122.couchbase.agora.io,81-70-71-42.couchbase.agora.io,82-156-9-186.couchbase.agora.io
CB_BUCKET=voice-agent-studio
CB_SCOPE=staging
CB_USERNAME=voice-agent-studio-staging
CB_SEARCH_ENABLED=true
CB_SEARCH_INDEX=studio_sessions_v2
```

Check the tool's actual variable names before using this tuple. A helper that
accepts only `CB_QUERY_ENDPOINT` needs an explicitly selected endpoint from the
list. SDK Search discovers its service topology; a working Query endpoint alone
does not prove KV or Search connectivity.

Ensure all advertised hostnames resolve in the process's own network environment,
including local operators and Pods. Use the deployment's hostAliases or the
approved hosts configuration where DNS does not provide these records:

| Address | Hostname |
| --- | --- |
| `49.232.236.50` | `49-232-236-50.couchbase.agora.io` |
| `49.233.208.122` | `49-233-208-122.couchbase.agora.io` |
| `49.233.32.129` | `49-233-32-129.couchbase.agora.io` |
| `81.70.71.42` | `81-70-71-42.couchbase.agora.io` |
| `82.156.116.98` | `82-156-116-98.couchbase.agora.io` |
| `82.156.9.186` | `82-156-9-186.couchbase.agora.io` |

Preserve unrelated ConvoAI and PipelineHistory host aliases. A successful lookup
on an operator laptop says nothing about a Pod or the source XDCR nodes.

## Worker and operations access

- Select `staging` or `production` explicitly before loading credentials. Build a
  clean child-process environment: remove inherited `CB_*` values, then load only
  the selected environment's approved configuration and credentials. Do not rely
  on an env-file loader that uses `setdefault`: stale shell values can silently
  win. Clear singular and plural Query/Search endpoint aliases together.
- Before writing, verify the selected cluster, bucket, scope and username without
  logging a password. Verify the actual runtime variable names and effective
  target, rather than assuming that a successfully loaded file was used. A fresh
  indexed Query and SDK KV/Search check cover different service paths.
- The staging runtime account is scope-limited and supports application KV
  reads/writes, indexed Query reads and Search. It is not an index migration or
  administration account. Use separately authorized operations credentials for
  collection/index DDL, FTS definitions, user management or replication. Do not
  grant runtime credentials administrative roles to make a helper pass.
- Use Studio APIs for business import, labeling and other API-owned workflows.
  This document does not authorize bypassing API contracts through direct writes.
  Shared-environment changes still follow the selected profile's environment
  ownership and assignment scope.
