# Validation evidence

All commands ran from `/home/adminuser/ovo`; the source checkout was read-only and the legacy comparison checkout was not edited.

- Baseline: `d73af7b2f43e9c506e915b214d926d3a1c0dc37e`; archive count 4371 minus 1418 audit files = 2953 files; executable mode audit passed.
- Builds: Go backend, gost, and gost/x; frontend `tsc --noEmit` and Vite build; documentation build.
- Tests: 333 installer cases (ordinary-user run with expected root-only skips), isolated root fixture cases, pinned Alpine lifecycle, backend handler suite, backend middleware raw-error gates, agent suite, and gost/x short suite.
- Rename gates: three-generation ownership and resolution stubs, identity key migration and rollback, flux/flmx agent migration and rollback, PostgreSQL URL identity preservation/fail-closed checks, and the original-owner red regression.
- Demo: standard page check plus direct `node/connlog`, `node/connlog-readiness`, `node/connlog-repush`, and `node/restart` checks; no `[demo] not recorded` responses.
- Security and consistency: secret-material scanner passed for 2947 tracked text files; forbidden numeric name scan passed; old-owner URL scan passed outside historical/fork exceptions; remaining `flmx`/`FLMX` lines are enumerated in `remaining-name-occurrences.tsv`; public-word and IP review ledgers are complete.
- Public artifacts: repository is public; `ovo-panel-backend`, `ovo-vite-frontend`, and `ovo-agent` are public; anonymous GHCR pull-token checks returned HTTP 200. The three images have amd64 and arm64 manifests.

The shell suite's root-only filesystem cases are recorded separately in `.git/evidence/root-fixtures.log`; the blanket root invocation is not claimed because this VM's `/home/adminuser` permissions prevent the non-root `nobody` gate. Muse must perform the live 094 menu 2/3/4 and M-02 deployment checks before merge.
