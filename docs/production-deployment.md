# Production deployment

Pushes to `master` deploy only after migrations, formatting, race-enabled tests,
`go vet`, `gosec`, `govulncheck`, and the production Docker build all pass.
CI and the production builder use the security-patched Go 1.26.5 toolchain.

The deploy job:

1. connects to the production droplet using repository secrets;
2. creates a compressed PostgreSQL backup and checksum;
3. switches the checkout to the exact tested commit;
4. builds and starts the application;
5. waits for container and end-to-end health checks; and
6. rebuilds the previous commit automatically if deployment fails.

Database backups are kept under `/opt/gmcl/backups` for 14 days. Application
rollback does not reverse database migrations, so migrations must remain
backwards-compatible and additive.

The scheduled `Production health` workflow checks the public health endpoint
every ten minutes. On failure it ensures the database is running and restarts
the currently deployed application and proxy containers without changing code
or data. If recovery does not restore service, it opens a GitHub issue.

Required repository secrets:

- `PROD_SSH_HOST`
- `PROD_SSH_USER`
- `PROD_SSH_KEY`
- `PROD_SSH_KNOWN_HOSTS`

Use the manual `workflow_dispatch` trigger to run either workflow on demand.
