# Security Policy / 安全政策

## Reporting a Vulnerability / 报告安全漏洞

**Please report suspected vulnerabilities privately.** Do not open a public Issue, Discussion, or Pull Request containing exploit details, an unpatched security weakness, or sensitive data.

**请通过私密渠道报告疑似安全漏洞。** 请勿在公开 Issue、Discussion 或 Pull Request 中披露未修复漏洞的利用细节、攻击方法或敏感数据。

**Private reporting / 私密报告入口：** [Report a vulnerability on GitHub](https://github.com/seakee/CPA-Manager-Plus/security/advisories/new)

This channel is intended for possible vulnerabilities in CPA Manager Plus (CPAMP), including its web UI, Manager Server, installers, packaging, and its integration boundaries with CPA / CLIProxyAPI. For an issue exclusively in the upstream CPA implementation, report it to the upstream maintainers. If the ownership is unclear, you may report it privately here so it can be triaged without public disclosure.

该入口适用于 CPAMP Web UI、Manager Server、安装器、打包及与 CPA / CLIProxyAPI 集成边界中的疑似安全漏洞。如果问题完全属于 CPA 上游实现，请联系上游维护者；若尚不确定归属，可以先私密提交给 CPAMP 维护者判断。

Public Issues are appropriate for ordinary bugs, feature requests, and deployment questions that do **not** disclose an unpatched security issue. Telegram and public Discussions are **not** security reporting channels.

普通 Bug、功能请求及部署问题仍可使用公开 Issue，但不得包含未修复安全漏洞细节。Telegram 群组和公开 Discussion **不是**安全漏洞报告渠道。

## Supported Versions / 支持范围

We accept private reports for the latest released CPAMP version and actively maintained development or pre-release branches, including v2 Beta while it is under development. Older versions may also be assessed for impact; whether a security fix is backported is determined case by case. This is not a promise of ongoing support for every historical release.

接受最新正式版 CPAMP，以及仍在维护的开发或预发布分支（包括开发阶段的 v2 Beta）的私密漏洞报告。旧版本也可报告以评估影响，但是否回移安全修复需逐案判断，不代表所有历史版本持续受到支持。

Refer to [Releases](https://github.com/seakee/CPA-Manager-Plus/releases) for published versions and fixed releases. Do not assume a development branch or an unreleased patch is an available security update.

正式版本和已发布的安全修复请以 [Releases](https://github.com/seakee/CPA-Manager-Plus/releases) 为准；开发分支或尚未发布的修复不等于可用的安全更新。

## What to Include / 建议提供的信息

Please provide whatever is available; a full proof of concept or CWE classification is **not required** to submit a report.

- Affected CPAMP version or commit, deployment mode, and relevant component.
- The suspected trust boundary or security impact, with reproduction information if safely available.
- Expected versus observed behavior and any mitigation already tested.
- Sanitized logs or screenshots, avoiding real keys, tokens, credentials, and personal data.

请尽可能提供：受影响的 CPAMP 版本或提交、部署方式、相关组件、安全影响、可安全分享的复现信息，以及经过脱敏的日志或截图。**不要求**报告者必须提供完整 PoC 或 CWE 编号。请勿直接提交真实密钥、Token、凭证或个人信息。

## Handling and Disclosure / 处理与披露

Maintainers will triage privately, assess scope and severity, and coordinate a fix or mitigation where appropriate. We may request additional information. Disclosure timing, security advisories, and reporter credit will be discussed through the private report; no fixed response or release deadline is guaranteed.

维护者会在私密渠道确认问题、评估影响与严重程度，并按需协调修复或缓解措施；必要时会要求补充信息。公开时间、安全公告及报告者致谢将通过私密报告沟通，**不承诺固定的响应或修复期限**。

Please allow coordinated remediation before publishing exploit details. If the issue has already been disclosed publicly, do not repost its sensitive details: open a private report linking to the public reference and inform maintainers. Closing a public Issue does not remove its previously disclosed content.

在协调修复完成前，请避免公开利用细节。若漏洞已经出现在公开 Issue 中，请不要再次转贴敏感细节；应通过私密报告提供公开链接并通知维护者。**关闭公开 Issue 并不会清除已公开的信息。**

Only test systems and accounts you own or are explicitly authorized to assess. Avoid accessing or modifying other people's data, disrupting services, or unnecessarily retaining sensitive information.

仅在自己拥有或已获明确授权的系统与账号上测试；不要访问或修改他人数据、影响服务可用性，或不必要地保存敏感信息。
