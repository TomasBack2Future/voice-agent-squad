# Studio Couchbase environments

Use this contract for Studio database operations and deployment
configuration. Check the selected environment's current deployment variables and
runtime configuration before connecting; historical reports and inherited shell
values are not environment selectors. Keep passwords in the approved local
secret store or deployment Secret, never in this package, command output or chat.

## Data-source boundary

| Data source | Cluster | Bucket / scope | Credential role |
| --- | --- | --- | --- |
| Studio staging | `couchbase-cn-2` | `voice-agent-studio` / `staging` | Dedicated runtime user `voice-agent-studio-staging` |
| Studio production | `couchbase-cn-2` | `voice-agent-studio` / `_default` | Dedicated runtime user `voice-agent-studio-production`; schema DDL uses separately protected operations credentials |
| ConvoAI RTSC / PipelineHistory | Existing ConvoAI data source | Its separately configured bucket / scope | Its own credentials; do not replace with Studio staging credentials |

The Studio staging and production cutovers keep the existing old-to-new XDCR
configuration. Do not change or delete the shared replication as a routine
application-connection repair. Old application writers stay stopped after each
cutover; the old source database can remain running. XDCR is one-way, so switching
an application back does not synchronize writes made on the new cluster: reconcile
those writes before a database rollback.

## Connection settings

Use the complete configuration tuple; changing only a bootstrap IP is insufficient.
The two Studio environments share a cluster and bucket but use different scopes
and application accounts. Select the exact environment before connecting.

### Staging

```dotenv
CB_QUERY_ENDPOINTS=http://49-232-236-50.couchbase.agora.io:8093,http://49-233-32-129.couchbase.agora.io:8093
CB_KV_CONNECTION_STRING=couchbase://49-233-208-122.couchbase.agora.io,81-70-71-42.couchbase.agora.io,82-156-9-186.couchbase.agora.io
CB_BUCKET=voice-agent-studio
CB_SCOPE=staging
CB_USERNAME=voice-agent-studio-staging
CB_SEARCH_ENABLED=true
CB_SEARCH_INDEX=studio_sessions_v2
```

### Production

For direct Studio production access after the verified cutover, use the
`_default` scope and the dedicated production runtime identity:

```dotenv
CB_QUERY_ENDPOINTS=http://49.232.236.50:8093,http://49.233.32.129:8093
CB_KV_CONNECTION_STRING=couchbase://49-233-208-122.couchbase.agora.io,81-70-71-42.couchbase.agora.io,82-156-9-186.couchbase.agora.io
CB_BUCKET=voice-agent-studio
CB_SCOPE=_default
CB_USERNAME=voice-agent-studio-production
CB_SEARCH_ENABLED=true
CB_SEARCH_INDEX=studio_sessions_v2
```

The production GitHub Environment's Query URLs use runner-reachable IPs;
Pod-local host aliases cover the six advertised Couchbase names. The production
NetworkPolicy must admit their IPs on the actual Query/KV ports and TCP 8094
for Search. Check both DNS and network policy from the Pod environment before
claiming connectivity. The `PRODUCTION_COUCHBASE_RUNTIME_USERNAME` and
`PRODUCTION_COUCHBASE_RUNTIME_PASSWORD` pair selects the scoped account in the
application Secret. The base `PRODUCTION_COUCHBASE_USERNAME` and
`PRODUCTION_COUCHBASE_PASSWORD` pair is also the scoped runtime identity, so a
fallback does not grant DDL access. Additive schema work uses the separately
protected `PRODUCTION_COUCHBASE_MIGRATION_DDL_USERNAME` and
`PRODUCTION_COUCHBASE_MIGRATION_DDL_PASSWORD` pair. A partial runtime pair fails
configuration preparation.

The production runtime account has `data_reader`, `data_writer`, `query_select`,
`fts_searcher`, and `query_use_sequential_scans`, each restricted to
`voice-agent-studio._default.*`. Sequential scans preserve the existing
application SQL behavior when replacing the former administrator account; do not
rewrite business SQL or grant administrator access merely to switch credentials.
For REST RBAC updates, a scope grant is written as
`query_use_sequential_scans[voice-agent-studio:_default]`; the role read-back
reports `collection_name: "*"`. Verify both allowed production queries and a
rejected cross-scope query after changing roles. Check staging's actual role
assignments separately rather than inferring them from production.

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
- Both Studio runtime accounts are scope-limited to their own scope and support
  application KV reads/writes, Query reads and Search. Production additionally
  permits scope-limited sequential scans as specified above. Neither is an index
  migration or administration account. Use separately authorized operations credentials for
  collection/index DDL, FTS definitions, user management or replication. Do not
  grant runtime credentials administrative roles to make a helper pass.
- Use Studio APIs for business import, labeling and other API-owned workflows.
  This document does not authorize bypassing API contracts through direct writes.
  Shared-environment changes still follow the selected profile's environment
  ownership and assignment scope.
