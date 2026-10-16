# Admin workspace release checklist

Date: 16 October 2026. Scope: Home, ineligible-player reports/cases/history, personal assigned cases and case detail. This is the release preparation and handoff record. Production deployment requires approval for the concrete candidate.

## Candidate and release record

The implementation is prepared on `codex/admin-ineligible-workspace`, in `C:\Users\warre\.codex\worktrees\admin-ineligible-workspace\GMCL`. It started at `44e4c4c85a290629bf660e624c8a751abded686d`. The original `E:\GMCL` checkout, including its unrelated untracked work, is preserved. Use the review's immutable head SHA and matching CI run for handoff.

Initial read-only inspection found cached master ahead of that starting baseline. The parent task then refreshed master and fast-forwarded this candidate worktree to `0c40366aa03060e8fda693f09d45eb52d5ebe48b` while preserving the uncommitted implementation. The reconciliation includes the upstream umpire corrections in migrations `0091`–`0093` (PRs #86–#88). The repository now contains 97 migration files. This identifies the local release base; it does not establish the deployed commit.

Before staging or release, confirm the candidate still incorporates then-current master, review the resulting diff and rerun the required checks. Record one immutable candidate SHA; staging, CI and release must use that same SHA. Do not release the old baseline or copy only selected files from a dirty checkout.

| Record | Value to fill before release |
| --- | --- |
| Candidate SHA and reviewed diff | Pending |
| Current master SHA used for reconciliation | `0c40366aa03060e8fda693f09d45eb52d5ebe48b`; recheck at handoff |
| Passing CI run for candidate | Pending |
| Staging URL, isolated database and data-volume names | Final local run `http://localhost:64974`, disposable `gmcl_handler_qa_20261016` schema `gmcl_qa_6566b811e59f49f8abbdb996f4338838`; task-local PostgreSQL 17, no application volume; cleaned after walkthrough |
| Staff walkthrough participants and result | Automated Chrome walkthrough as synthetic owner, designated reviewer, final issuer, approval-only and no-grants accounts: passed; owner previously accepted layouts; release approval pending |
| Deployment operator and project-owner approval | Pending |
| Previously deployed SHA and image/build identifier | Pending |
| Pre-deploy backup path, checksum and restore check | Pending |
| Deployment time, smoke-test result and rollback decision | Pending |

## Preconditions

- [x] Candidate contains the complete implementation, new source/test files, styles and CI changes. No generated captures, test databases, runtime credentials or task-local tools are included. Test-only fixtures use explicit synthetic values.
- [ ] No workspace-specific migration or infrastructure is required by this patch. Compare the target's migration set with the actual deployed set: upstream migrations `0091`–`0093` add a corrections table and update matching umpire reports if not already applied. Keep those upstream changes intact; application rollback must not assume their schema or data changes are reversed.
- [x] Existing permission grants remain unchanged. Full `/admin/ineligible` access and its CSV export remain triage-only. Staff with investigation/approval access retain their existing personal index and case reads. Navbar visibility follows those gates on the current workspace pages; it does not grant access.
- [x] Home, personal index, ineligible reports/cases and case detail use the capability-aware workspace navbar. Unreviewed older pages retain their existing navbar and are a later cleanup, not evidence that a grant should be widened.
- [x] Source-specific decision rules remain intact: Dave/Warren's designated ineligible approval may include their own proposal; Denver's final issue is separate. General approval alone does not confer designated approval or final-issue authority.
- [x] Owner confirms the scope and presented layouts. The accepted closed-season state has no current cases; synthetic scenarios exercise active workflows now. Do not treat a zero active queue as proof that no history or follow-up exists.

## Technical gates

Local checks already cover production renderers, isolated PostgreSQL query populations, complete real migrations with pgvector, authenticated read handlers, and closed-season states. These checks are useful evidence; they do not replace the exact release candidate's CI run or a controlled successful-command walkthrough.

Current local evidence after reconciliation:

| Check | Observed result |
| --- | --- |
| Home/nav/closed-season link regression | Passed against the real migrated schema; rankings, ledger and weekly records/detail/filter/back links retain the shown season |
| Release toolchain/dependencies | Go 1.26.9 in CI/Docker; `golang.org/x/text` 0.41.0 and transitive `golang.org/x/sync` 0.22.0 |
| `gosec` | 0 findings with the repository's existing configured exclusions |
| `govulncheck` | 0 reachable vulnerabilities; 22 required-module advisories concern uncalled code, so this is not a claim that dependencies have no advisories |
| Chosen staff rehearsal | Isolated local browser staging using actual handlers and synthetic accounts/data |
| Full final Go 1.26.9 test/vet/build result | Passed: full race suite with integration DSNs unset, selected PostgreSQL/authenticated/action/query race suite, vet and build; GCC 16.2.0 supplied race support |
| Complete migrations | All 97 unchanged files passed with real pgvector 0.8.7/HNSW in fresh disposable schemas |
| Exact-commit remote CI and production container build | Pending; local Docker image build unavailable because the daemon is not running and was left untouched |

Final local QA evidence is recorded in `E:\GMCL\tmp\admin-ineligible-verification\final-go1269-verification.json`, `final-go1269-race.log` and `final-go1269-pg-race.log`. These are task-local generated evidence, not release source files. The final local browser harness is rebuilt from the same checked source; the staff/browser outcome is recorded separately.

- [x] Record `go version` for local verification: `go1.26.9 windows/amd64` with GCC 16.2.0. Require CI and the production Docker builder to use the same reviewed Go version.
- [ ] The reconciled candidate passes changed-file formatting, `go test -race ./...`, `go vet ./...`, configured `gosec`, `govulncheck` and the production Docker build in `.github/workflows/ci.yml`.
- [x] Local selected integration tests execute rather than skip: fresh migrated schemas and TEMP fixtures ran against disposable PostgreSQL 17, including all 97 unchanged migrations through the actual runner with real pgvector/HNSW.
- [ ] Confirm the exact candidate's remote CI integration tests also execute. CI now supplies `TEST_DB_DSN` and the separate loopback `gmcl_handler_qa_` database through `GMCL_HANDLER_TEST_DSN`; configuration is not proof of a completed remote run.
- [x] Local selected tests reconcile matching totals, pages and CSV across manual/split cases, live/training/history, assigned/action scopes, current proposal revisions, source changes and failed reads, including the report population above 2,000 rows and closed-season authenticated Home tests.
- [x] Successful authenticated command tests rehearse report-to-case, reply review, owner notice review, designated approval, separate final issue, changed-source review, completed-case follow-up and exact outbox state. Browser forms separately pass reply review, proposal, three saved notices, owner review, approval, separate issue and follow-up completion.
- [x] The chosen local staging browser walkthrough passes real password verification, session and CSRF behavior, role-specific navigation, actual CSV download parity (nine matching case references), history/training separation and 390px layouts. Its fixture-only password login deliberately disables email 2FA. Hosted HTTPS/proxy configuration, real email 2FA and recipient transport are not certified by this local rehearsal; verify the unchanged deployed boundaries during the authorised release smoke check.

Final browser receipts are `staging/live-browser-receipts.json` and `staging/live-browser-finish-receipts.json` under the task-local evidence directory. The final dataset used a schema-valid ineligible training designation, separately excluded from live work. Independent review found and then verified the completed-season weekly drilldown correction; no remaining blocker was reported in that scoped review.

Do not mark these gates complete solely because local tests passed before the final candidate existed. The remote CI run and a successful production Docker build are pending until they are actually executed for that candidate.

## Staging isolation and staff walkthrough

The user selected isolated local staging. [The opt-in walkthrough harness](../internal/httpserver/admin_workspace_staging_server_test.go) binds only to loopback, mounts the actual admin router, uses a fresh migrated schema and task-local evidence files, and exposes synthetic accounts for a bounded walkthrough. It does not construct `NewServer` or start workers, importers or external clients. Fixture login uses the real password/session handlers with email 2FA explicitly disabled; the harness is test-only and adds no production fixture endpoint.

Record the generated local URL and expiry from its task-local manifest. Passwords and signing secrets are synthetic and local; do not reuse them elsewhere. Reset by stopping the harness and starting a new isolated schema. Keep staged recipients separate from real league contacts and retain the harness's synthetic-only recipient check and POST allowlist.

If container-based staging is used later, use a dedicated database, application data volume, origin, signing secrets and synthetic staff/club records. Do not point that application or n8n at production services or secrets.

The repository's production `docker-compose.yml` explicitly enables the sanctions outbox scheduler, ineligible outbound email and bootstrap import, and defaults the weekly sync on. Its application URL environment entries also override `.env`. The existing rules-staging script is for another workflow and is not a complete admin-workspace staging setup. Editing only `.env` is insufficient for those compose entries.

Before starting a container-based staging application, override and inspect the resolved application environment:

| Setting | Required initial staging value |
| --- | --- |
| `APP_ENV` | `staging` |
| `DB_DSN`, database/data volumes | Dedicated staging only |
| `PUBLIC_BASE_URL`, `PUBLIC_ALT_BASE_URL`, `SANCTIONS_BASE_URL` | Actual staging hostname(s) |
| `SANCTIONS_EMAIL_DISABLED` | `true` |
| `INELIGIBLE_OUTBOUND_EMAIL_ENABLED` | `false` |
| `SANCTION_OUTBOX_SCHEDULER_ENABLED` | `false` |
| `INELIGIBLE_IMPORT_ENABLED` | `false` |
| `INELIGIBLE_BOOTSTRAP_IMPORT_ENABLED` | `false` |
| `STARRED_WEEKLY_SYNC_ENABLED` | `false` |
| n8n/internal callers and external service credentials | Absent or isolated staging equivalents |

For container-based staging, use the same reviewed compose files for inspection and startup. Inspect only the listed non-secret settings; do not paste a full resolved environment containing credentials into logs or review notes. Do not enable sending to demonstrate a screen. If transport is explicitly rehearsed later, use an isolated test mailbox/sink and record the intended messages and attempts before enabling that rehearsal.

Follow the staging walkthrough with representative existing permissions:

1. **Home:** personal actions precede league totals; zero counts are valid and source failures say unavailable. Completed reporting remains selected when a future season is configured. No rating sample is distinct from an unavailable sample. Required = received + exempt + missing; stored submissions remain separately labelled raw records. The statistics link opens Home's season-statistics disclosure; rankings, legacy ledger and weekly records retain the shown season. Following a week, filtering clubs, clearing the filter and going back retain that explicit season even when another season has the same week number.
2. **Navigation/access:** try triage-only, investigator-only, approver-only, final-issuer and no-sanctions-access accounts. Every offered protected menu link opens for that account. Direct forbidden URLs stay forbidden. Logout and mobile dropdowns work; no grant is added merely to make a menu item work.
3. **Reports and cases:** report triage is distinct from case progress. Mine, Needs my action and All permitted scopes retain their meaning. Manual and split cases each appear once. Search, fixture dates, training, history, paging and reset controls retain the intended population.
4. **Counts and CSV:** open each Home count/destination using the same account and scope. Count rows across pages, then compare the export's data-row count and identifiers. CSV includes all matching cases, not just the visible page; training and history stay scoped and spreadsheet text stays safe. Weekly reporting cards open the selected week's Compliance source records; they are not individual status-filtered exports.
5. **Case detail:** the first action matches the current records and viewer. Save/check each audience notice, complete owner review on the current revision, hand to designated approval and then the separate issuer. A changed approved source blocks final issue until source review. Completed cases keep task/delivery history without becoming active investigation again.
6. **Return context:** open a case from a filtered page, Home action list and personal index. Back to results preserves the supported filter/page/section after an action redirect. New tabs and expired navigation cookies have a sensible permitted fallback. External return URLs are not followed.
7. **Closed season:** active queues can be empty while historical cases, delivery exceptions and future-effective tasks remain. A configured future season must not capture completed reporting. Explicit season archives keep their existing exclusion policy.
8. **Operational finish:** record any unexpected queued messages, duplicate events, access errors, failed reads or unexplained count differences. Block release until each has a resolved cause or an explicitly accepted limitation.

The first real next-season cases require an operating reconciliation when they exist. This is not a reason to defer synthetic staging validation now.

The separately named Executive report remains an existing destination with its own upcoming-first default. This slice does not change that policy or make it the source of Home's closed-season statistics. Review the Executive report's season-selection/default behavior in the wider statistics work.

## Controlled rollout

The `deploy-production` job runs automatically after a passing build-test job on a push to `master`. A push/merge to master is therefore a production deployment action, not a harmless way to request validation. Do not perform it without the explicit release handoff.

1. Obtain the project owner's approval for the concrete candidate and walkthrough result. Record the operator and the previous deployed SHA from the actual host; local cached references do not establish production state.
2. Use the existing exact-commit pipeline. `scripts/deploy.sh` fetches master, checks the target is on master, rejects tracked production edits, creates a database dump/checksum, switches to the tested commit and rebuilds the application. Do not use `ALLOW_UNMERGED_COMMIT=1` to bypass the reviewed release path.
3. Verify the backup is nonempty, the checksum is recorded and the restore procedure has been checked against a disposable database. Backups are kept under `/opt/gmcl/backups` for 14 days by the current script; retain the relevant release evidence beyond that window if needed.
4. Confirm container health and both public endpoint checks pass. Then perform read-only authenticated smoke checks of Home, personal index, reports/cases/history, CSV scope and a case detail using permitted accounts. `/health` alone does not validate this workspace.
5. Compare operational totals to their authorised source records. Review application logs for new query failures, unexpected 403/500 responses, and unexpected outbox/delivery activity. No current cases is a valid expected result; unavailable is not.
6. Record the result and keep the previous release available until the walkthrough checks are stable. Do not combine this UI release with changes to recipients, permissions, import activation, scheduling or outbound email switches.

## Rollback

Rollback triggers include unexplained count/source divergence, missing permitted work, blocked legitimate owner/approval/issue actions, newly offered forbidden destinations, broken return context or unsuccessful authenticated smoke checks. Pause the walkthrough and identify the failing candidate rather than changing permissions to conceal a failure.

`scripts/deploy.sh` automatically rebuilds the previously deployed commit when build, container health or its end-to-end health check fails after deployment starts. A functional workspace failure after those health checks requires an operator decision; the scheduled health-recovery workflow only restarts current containers and does not roll code back.

For an authorised functional rollback:

1. Record the failing SHA, time, logs and any case/outbox activity. Confirm the previous working SHA from `.previous-successful-deploy` and the release record; do not assume a marker is current without checking it.
2. Reuse the deployment script with `DEPLOY_COMMIT` set to that verified previous commit and the actual production `APP_DIR`. Its master-ancestry check, backup and health checks still apply. Example operator command, to be run only after handoff:

   ```bash
   APP_DIR=/opt/gmcl DEPLOY_COMMIT=<verified-previous-sha> bash /opt/gmcl/scripts/deploy.sh
   ```

3. Verify public health, signed-in Home and permitted case/list reads, then reconcile the affected records and queued messages. Restoring application code does not undo a decision, email already sent or other data written during the release window.
4. Do not restore the production database as the default UI rollback. This workspace patch adds no migrations, and application rollback does not undo any newly applied upstream umpire corrections. A data restore would discard newer activity and requires its own incident plan and explicit authorisation. Do not delete or replay outbox/audit records to make totals match.
5. Record the rollback outcome, leave production flags and grants at their approved values, and return the corrected candidate through CI and staging before another release.

## Source of the procedure

The checklist is grounded in [the implementation notes](ADMIN-WORKSPACE-IMPLEMENTATION-NOTES.md), [production deployment documentation](production-deployment.md), [CI and automatic deployment](../.github/workflows/ci.yml), [exact-commit deployment and rollback](../scripts/deploy.sh), [health recovery](../scripts/recover-production.sh), [production compose defaults](../docker-compose.yml), and the existing route guards in `internal/httpserver/admin.go`. Host state and production approval must be verified at handoff. Draft review/CI validation is separate from the master push that deploys production.
