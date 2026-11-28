# OVO 2.1.34 rename report

Content baseline: `d73af7b2f43e9c506e915b214d926d3a1c0dc37e`. Target: public `yhfc1775-maker/ovo`. The new repository has an empty root commit followed by one snapshot commit on `rename/ovo-2.1.34`; the source repository history is not a parent. Commit/tree identities and CI URLs are recorded in the PR description after the snapshot is committed.

## Baseline and source preservation

Source `main`, `origin/main` and `HEAD` were verified against the baseline. The user authorized fast-forwarding only the local source `main` reference. The 33 modified and 6 untracked source files were inspected as P1 performance work and left untouched. Source status remains 1418 audit deletions, 33 modifications and 6 untracked files.

The archive contains 4371 tracked files; removing the three specified audit directories leaves 2953 physical files before initialization. Every extracted executable bit agrees with the Git tree, including 21 executable shell scripts. `panel_install.sh` is executable; `install.sh` remains non-executable. The target main contains only `Initial commit`, with an empty tree. Snapshot author uses the existing Codex Git configuration: `admin <admin@example.com>`.

## Closed exception checklist

| ID | OVO implementation and retained compatibility |
|---|---|
| E1 | Canonical keys are `OVO_*`. Backend and agent readers fall back to `FLMX_*` only when the current key is absent, including an explicitly empty current value taking precedence. Shell startup imports legacy environment keys; `.env` uses the same read order. Both identity migration paths rename keys without rewriting their raw values, including UUIDs. Compose interpolation changes in the same update operation. Version writes are `OVO_VERSION` plus `FLMX_VERSION`. Deployment download credentials are removed; public downloads do not read a PAT. |
| E2 | Keep the independent sing-box fork repository, its original whitelist entry, `1.14.4-flmx1` and other fork suffixes, `IsForkVersion`, and all three suffix regexes. |
| E3 | Keep flux/flvx historical protocol and layout recognition. Retired `FLUX_*`/`FLVX_*` configuration reads are not restored. |
| E4 | Current nft identifiers use `ovo_`, `ovo_kernel_*`, `ovo_hop_probe` and `ovo_quota_*`. The panel's `ovo_<hash>` table and agent's `ovo_kernel_<hash>` table are different namespaces. |
| E5 | New PostgreSQL user/database: `ovo_panel`; scratch: `ovo_reconcile_scratch`. Existing `.env` values and `DATABASE_URL` remain authoritative; the rename does not rename or migrate an existing database. Legacy placeholder passwords remain rejected. |
| E6 | Preserve `flmx:user-mark-v2:`, `flmx:clash-api-v1:`, `flmx:node-secret-v1:`, advisory lock `flmx:name:`, and persisted `flmx_traffic_`/`flmx_connlog_` spool names. |
| E7 | Preserve deployed HMAC domains `flmx-kcp-key`, `flmx-ss-upsk`, `flmx-scoped-sub-v1`, `flmx-inbound-import-preview-v1`. |
| E8 | Issue `ovo-subsig-v3`; verify previous flmx v3 through `subURLSigV3FlmxForToken`, then earlier formats. The flmx fallback respects `flowV1LegacySunset` (2027-02-02). |
| E9 | Panel accepts `X-Ovo-Seq`, `X-Flmx-Seq`, `X-Flvx-Seq`, in that order. Agent sends `X-Ovo-Seq` and `X-Flmx-Seq`. |
| E10 | Historical flmx plain and isolated nft tables are recognized for migration. Agent persists a separate `RenameMigrated` flag after a successful atomic apply. Panel cleanup requires existing ownership proof, and targets this install's previous derived table names. Fresh installs do not claim foreign tables. |
| E11 | `LEGACY` flux and `LEGACY2` flmx deployment identities are recognized. Image ownership requires the old owner paired with the explicit legacy image-name set. Old owner + new name and foreign owner + old name are rejected. Go and shell container rename predicates cover empty, flux and flmx default names, preserving custom/current names. Cleanup tries both owner prefixes and only warns on deletion failure. |
| E12 | Add the complete flmx agent helper family and direct flmx→ovo migration, alongside flux→ovo. Footprints are frozen before migration; flmx is attempted before flux. Snapshots fail closed, and rollback restores the old unit bytes and layout. The flmx rollback service template matches the baseline current service, including resource limits. Go recognizes all three binary/layout generations. |
| E13 | Frontend internal identifiers are `ovo:entities-changed` and `ovo:entity-revision`. |

Every remaining source occurrence is enumerated in `rename-evidence/remaining-name-occurrences.tsv`. Historical event records retain their original references. The inventory excludes its own generated ledgers to avoid recursive entries. `rename-evidence/public-word-review.tsv` records private/credential-related words individually; these include language access modifiers, private-key/SSRF terminology, synthetic security tests, historical records and GitHub Actions publishing credentials. Installation/download instructions use anonymous release assets and SHA-256 checks.

## M-01–M-20 application

| Lesson | Applied change / evidence |
|---|---|
| M-01 | Go and shell image ownership share the new-prefix + paired-legacy rules. Both legacy probes are reachable. Original prefix-only ownership reproduces red after changing REPO; the three-generation regression passes green. |
| M-02 | 094 实测中，cwd 不 export、显式 `PANEL_BACKEND_CONTAINER`、任意目录三种变体的 resolve 均通过：`.env` 键、Compose 标签和全局探针共同指向该部署；该结果只适用于该 094 夹具，不推广为任意环境保证。文档继续保留显式 export 形式作为最稳妥写法。 |
| M-03 | Repository is public. Three public GHCR packages are required; `scripts/verify-ovo-access.sh` checks anonymous pull tokens, and the publishing workflow checks each pushed package anonymously before release. Package visibility must be changed in package settings: the proposed visibility REST path returns 404 and no documented REST/GraphQL mutation was found. |
| M-04 | Remove deployment PAT loading/persistence and Docker login fallback; retain local image-inspect guards. Update public-download and fail-before-config-write tests. |
| M-05 | Import the public manual at `doc/public/manual/index.html`; its embedded screenshots are extracted byte-for-byte into adjacent image files (hash ledger retained). |
| M-06 | Manual text targets 2.1.34, uses pinned release assets plus checksum validation, and labels 2.1.23 screenshots as historical operation-location references requiring current-version confirmation. F-21 behavior was not changed. |
| M-07 | Add the flmx migration slot and helper family from the flux structure, with rollback template checked against the original current service. |
| M-08 | Release notes have What's Changed and the 14-asset list derived from the upload steps. No date placeholder. |
| M-09 | Shell and Go helper cleanup explicitly try new and old owner prefixes; failures warn without rollback. |
| M-10 | `forwardDiagnoseStream` checks repository-tainted errors before considering typed validation errors. Fixed stream-support messages remain unchanged. |
| M-11 | Mirror purge errors use fixed user messages and log the underlying error; invalid versions keep a fixed validation message. |
| M-12 | Reapply the supplied four-file CI diff under OVO names: admin package synthesis, token-expiry protection, logged-in-only update check and repository error handling. |
| M-13 | Add demo answers for connlog/readiness/repush/restart; directly click the actions in the built demo in addition to the standard page walk. Missing forward recordings warn explicitly. |
| M-14 | Correct panel-versus-agent nft table descriptions and migration names. |
| M-15 | Import format displays “OVO格式” while its key remains `ovo`. |
| M-16 | Freeze current/flmx/flux agent footprints in install and update; migrate each old generation directly, with a fail-closed occupied-target check. |
| M-17 | Upgrade documentation names both old generations and lists `yhfc1775-maker/ovo` as the current repository/owner. |
| M-18 | Restore push, pull_request and workflow_call CI; official runners only. ci-build has 9 ubuntu-latest + 2 ubuntu-24.04 entries. All external actions remain pinned to full SHAs. |
| M-19 | `githubRepo` points to `yhfc1775-maker/ovo`; keep the independent fork source unchanged. |
| M-20 | Docs base and demo are `/ovo/` and `/ovo/demo/`; deployed-version assertion downloads every JS asset selected from the demo HTML, with no first-asset shortcut. |

## Validation and practical limits

Detailed results, command identities and selected red/green evidence are in `rename-evidence/validation.md` and adjacent artifacts. No production host or 094 test machine was accessed. The handler raw-error gates establish their measured coverage; the previously untraced family of 180 sites is not claimed individually reviewed.

The shell suite defines and calls 333 cases. Run it as the ordinary user with passwordless sudo for self-update fixtures; four root/filesystem-only cases are additionally executed as root in isolation. A blanket root run cannot exercise the non-root gate in this VM because its home path is not traversable by nobody; its failure is retained as diagnostic evidence, and all four missing root cases are run separately without skipping. Alpine OpenRC lifecycle runs in the pinned Alpine container.

Three-generation stubs exercise deployment selection and the shared resolution gates, ownership, key migration and direct agent migration/rollback. They do not claim real Docker/Compose destructive update, uninstall or PostgreSQL migration against a live deployment. Those live gates remain Muse's pre-merge duty.

## Muse 094 pre-merge plan

1. Prepare isolated flux, flmx and ovo deployment forms. The flmx fixture must use container `flmx-panel-backend`, image `ghcr.io/iiiiiiiii7125-art/flmx-panel-backend:2.1.29`, and the old Compose working-directory/project/service labels. Record the equivalent triple for flux and ovo.
2. For each, test menu 2 update, menu 3 uninstall selection/confirmation gate, and menu 4 migration selection/confirmation gate. Confirm foreign deployments and both mismatched prefix/name pairs are refused. Preserve real data backups and verify rollback before accepting the rename.
3. Repeat each selection gate in all M-02 forms: cwd without export, explicit export, and arbitrary directory. The 094 fixture passed all three because its `.env` key, Compose labels and global probe all matched; record that as fixture-specific evidence only. Documentation retains the explicit export form as the safest invocation, e.g. `cd /root/flmx-panel && PANEL_BACKEND_CONTAINER=flmx-panel-backend ./panel_install.sh`.
4. Compare the remote PR head tree to the recorded snapshot tree. Merge only with squash or fast-forward; main must then have two commits and the expected tree. Codex does not merge, tag, release or delete the branch.

## Snapshot handoff

The repository and all three GHCR packages are public; anonymous token checks returned HTTP 200 and the multi-architecture image manifests were pulled anonymously. Local validation is recorded in `rename-evidence/validation.md`; final commit/tree identities and CI status are supplied in the PR description after the single snapshot commit. Muse must still perform the live 094 checks listed above. Release 2.1.34 is reserved for Muse after audit and merge; this task creates no Git tag or GitHub Release.
