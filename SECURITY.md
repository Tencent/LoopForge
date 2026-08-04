# Security Policy

English | [简体中文](SECURITY.zh-CN.md)

## Reporting a vulnerability

Please use GitHub Security Advisory's private reporting feature to contact maintainers. Do not submit the following in public issues, discussions, or pull requests:

- SecretId, SecretKey, Token, Cookie, or passwords;
- Internal domain names, servers, clusters, buckets, topics, or pipeline IDs;
- Unredacted logs, user data, or configuration files.

Reports should include the affected version, reproduction conditions, potential impact, and suggested fix. Maintainers will acknowledge receipt first, then schedule fix and disclosure based on risk.

## Supported versions

Only the latest stable version and the default branch are maintained. Security fixes go into subsequent patch releases.

## Deployment responsibility

This repository does not provide production credentials or operational configuration. Deployers should use least-privilege identities, short-lived credentials, or a secure Secret Manager, and ensure local configuration is excluded by `.gitignore`.
