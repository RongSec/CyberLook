# 第三方组件与数据授权声明

> 本项目以 **GPL-3.0** 开源、不作商业用途。本文件登记随项目分发的第三方代码与数据及其授权。

## 1. 指纹规则数据（随项目分发）

| 组件 | 来源 | 授权 | 落地位置 | 说明 |
|---|---|---|---|---|
| FingerprintHub `web_fingerprint_v4.json` | [0x727/FingerprintHub](https://github.com/0x727/FingerprintHub) | **MIT** | `data/fingerprint/` | 3450 条 Web 指纹（word / regex / favicon） |
| FingerprintHub `service_fingerprint_v4.json` | 同上 | **MIT** | `data/fingerprint/` | 11938 条 service 探针（**全量导入**，未裁剪） |
| 授权原文 | 同上 | MIT | `data/fingerprint/LICENSE.md` | 与规则文件同目录保留 |

规则版本：`web_fingerprint_v4` / `service_fingerprint_v4`（2026-11-09 获取，仓库当日仍有提交）。

## 2. Go 依赖（直接依赖，宽松授权）

`gin`、`gorm`、`golang-jwt/jwt`、 `robfig/cron`、`creack/pty`、`gopsutil`、`excelize`、
`modernc.org/sqlite`、`golang.org/x/*` —— 均为 MIT / Apache-2.0 / BSD-3-Clause 类，
与 GPL-3.0 兼容。完整清单见 `go.mod` / `go.sum`。

**当前未引入**：`wappalyzergo`（代码 MIT、规则数据 GPL-3.0）、`fingerprintx`（其 HTTP 插件
传递依赖 wappalyzergo）、`masscan`（AGPL-3.0）、`gogo` / `observer_ward` / `WhatWeb`（GPL）。
本项目已是 GPL-3.0，引入这些不再有协议冲突，但引入前仍需在本文件补登记来源。

## 3. 关于 GPL 数据的边界（写给以后的维护者）

- **MIT 包装不能改变数据集的授权**：`wappalyzergo` 代码是 MIT，但它内嵌的规则数据来自
  `enthec/webappanalyzer` 与 `HTTPArchive/wappalyzer`，两者均为 **GPL-3.0**。
- 本项目整体 GPL-3.0 → 可以合法分发这类数据；但**若未来出现闭源/一体机交付需求**，
  必须先移除 GPL 数据并把协议重新评估，否则会触发 GPL 的源码提供义务。
- 当前实现刻意保持"零 GPL 数据依赖"：协议识别使用自研被动探针（`internal/svcid/probe.go`），
  Web 识别使用 MIT 的 FingerprintHub 规则。这样即使将来改授权，也不用返工。

## 4. 其他

- chromium（运行时镜像内通过 Debian apt 安装）：BSD 类/多协议组合，由 Debian 分发。
- TscanPlus（历史扫描引擎，二进制不随本仓库提交）：第三方免费版，授权未查证；
  自研扫描器（`internal/portscan`）正在替代它。
