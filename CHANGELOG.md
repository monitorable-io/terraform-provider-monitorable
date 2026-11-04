# Changelog

All notable changes to this provider are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

## [0.1.0] - Unreleased

Initial release. The date is set when the `v0.1.0` tag is cut.

### Added

- Resources: `monitorable_server`, `monitorable_server_alert_template` and `monitorable_server_mute`.
- Data sources: `monitorable_server`, `monitorable_servers`, `monitorable_alert_template`, `monitorable_alert_templates` and `monitorable_notification_channels`.
- Provider configuration through `api_key` and `endpoint`, or the `MONITORABLE_API_KEY` and `MONITORABLE_ENDPOINT` environment variables.
- Import for all three resources.
- Automatic retry of rate-limited (`429`) and transient (`502`, `503`, `504`, network) failures.
- Signed release archives for Terraform 1.5 or later and OpenTofu 1.6 or later (plugin protocol 6).
