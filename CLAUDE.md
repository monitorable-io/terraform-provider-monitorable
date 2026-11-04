# CLAUDE.md

Public Terraform provider for Monitorable (`registry.terraform.io/monitorable-io/monitorable`),
built on terraform-plugin-framework (protocol 6). It manages servers, alert template
assignment and maintenance mutes through the Monitorable REST v1 API.

- The client calls only endpoints present in `openapi.json` (pinned by a contract test).
- Environment variables: `MONITORABLE_API_KEY`, `MONITORABLE_ENDPOINT`, `TF_ACC`.
- Never commit a key. The in-process fake accepts `mon_test_fake` keys only.
- Commit messages and PR titles follow Conventional Commits; no generated-by or co-author trailers.
- Work on a branch and open a PR; `validate` must be green before merge.
- Releases are cut by a `v*` tag only after the maintainer's explicit permission.

Make targets: `build`, `test`, `testacc`, `docs`, `lint`.
