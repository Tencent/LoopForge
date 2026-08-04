# Security Policy

[English](SECURITY.md) | 简体中文

## Reporting a vulnerability

请使用 GitHub Security Advisory 的私密报告功能联系维护者。不要在公开 Issue、Discussion 或 Pull Request 中提交：

- SecretId、SecretKey、Token、Cookie 或密码；
- 内部域名、服务器、集群、Bucket、Topic 或流水线 ID；
- 未脱敏的日志、用户数据或配置文件。

报告应包含受影响版本、复现条件、潜在影响和建议修复方式。维护者会先确认收到，再根据风险安排修复与披露。

## Supported versions

目前仅维护最新稳定版本和默认分支。安全修复会进入后续补丁版本。

## Deployment responsibility

本仓库不提供生产凭据或运维配置。部署方应使用最小权限身份、短期凭证或安全的 Secret Manager，并确保本地配置被 `.gitignore` 排除。
