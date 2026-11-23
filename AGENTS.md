# Vastora Agent Rules

- Do not preserve obsolete API or implementation compatibility. Remove superseded implementations outright; do not add aliases, dual code paths, or runtime fallbacks.
- Released Center database schemas must use tested, forward-only migrations. Back up before migrating, fail closed on migration errors, and do not support automatic database downgrade.
- Choose the simplest implementation that fully satisfies the current requirements. Avoid speculative abstractions, unnecessary indirection, and redundant configuration layers.
- Build the system incrementally in vertical slices. Make the smallest end-to-end version work before adding more layers, and never dismantle working functionality to accommodate unfinished complexity.
- Keep components modular and maintain a clear separation of concerns.
- Prefer mature, actively maintained libraries. Do not reimplement established functionality without a clear, documented reason.
- Inspect the capabilities of existing project dependencies before adding a new package or writing custom code. Do not assume the required capability is missing.
- Make architectural decisions for the long term. Do not introduce temporary designs with the intention of replacing them later.
- Study how mature products solve the same problem and follow proven patterns instead of inventing a solution from scratch.
- Do not proactively run local tests, builds, linters, type checks, or browser QA. Only run them when the user explicitly requests local verification. Repository-required CI gates may still run as part of an explicitly requested push, merge, or release workflow.

## GitHub releases and Actions storage

- Before manually dispatching CI, inspect runs for the exact commit and required
  scope. Reuse an already queued, running or successful applicable full check;
  do not dispatch a duplicate for observation or cache warming. Retry only a
  failed/interrupted required run or an explicitly requested benchmark. Main
  cache warming compiles only; PR checks and main CodeQL remain required.

- Use Release Please v5, pinned to a reviewed full commit SHA. Version PR
  creation/updates use a short-lived, repository-scoped installation token from
  the minimal-permission organization release GitHub App (`petauron-nova`:
  `contents: write` and `pull-requests: write` via `RELEASE_APP_ID` and
  `RELEASE_APP_PRIVATE_KEY`) so normal `pull_request` CI and CodeQL checks run
  automatically; do not add a personal PAT. Actual release creation and asset
  publication continue using the built-in `GITHUB_TOKEN`. Grant only the job
  permissions needed; never weaken branch protection to enable releases.
- Release through the generated version PR and the repository's Release Please
  config/manifest. Use Conventional Commits, including the final squash title;
  do not hide a releasable fix under a `ci:` or `chore:` title. Do not manually
  bump versions, move tags, or add a second tag-triggered release path.
- Required checks must represent real checks on the exact version PR head SHA.
  Metadata validation is a separate check, not a substitute for CI or CodeQL;
  never manufacture successful required-check results.
- Publish only from an immutable, checked commit on protected `main`. Use the
  Release Please output SHA/tag throughout checkout, build, provenance and
  publication; fail closed on identity/version mismatches or failed checks.
  Do not assume a tag created with `GITHUB_TOKEN` triggers another workflow.
- Keep releases as drafts until all required builds, integrity/provenance checks
  and uploads succeed. Retain only the distribution files and metadata required
  by users or verified consumers. Retry failed jobs or an explicitly documented
  recovery flow; never overwrite an already published release or move its tag.
- CI and manually dispatched maintenance workflows do not retain downloadable
  Actions artifacts: no binaries, UI bundles, browser evidence, source patches,
  workspace copies or build records. Do not add upload/download-artifact steps
  or retention-based exceptions for these files. Only explicitly requested test
  coverage reports may be uploaded, with narrowly scoped contents and short,
  documented retention. Keep the actual tests and required CI gates.
- Set `DOCKER_BUILD_RECORD_UPLOAD=false` for Docker build actions. Keep bounded
  dependency/build caches for CI speed; caches are not release artifacts.
  Stage necessary cross-job release files directly in the draft Release rather
  than keeping duplicate Actions artifacts.
- Artifact cleanup must enumerate exact targets, skip active runs and artifacts
  required to recover failed releases, and preserve published Release assets and
  caches unless separately authorized. Never include credentials, production
  data, private host details or full subscription URLs in files or logs.
- Distinguish workflow edits, successful CI, successful publication and production
  deployment in completion reports. A green preparation job with skipped publish
  jobs is not proof of a release. Documentation edits do not authorize a push,
  merge, release, signing operation or production deployment.

- Keep Center/Agent program releases separate from signed application catalog
  publication and managed application upgrades. Preserve the catalog's protected
  signing and monotonic revision rules; an app-only update must not require a
  Vastora program release or automatically upgrade an installed instance.

## Petauron 团队：Issue 跟踪与完成标准（MVP）

- 开发每项已获授权的功能或缺陷修复前，先查找并关联对应仓库的现有 issue；没有合适的 issue 时先创建，简要写明用户目标、最小范围和完成标准（仅覆盖代码/文档实现与自动化测试或 CI 验证，不写入线上部署或生产环境验收要求）。不要重复创建，也不要把创建 issue 当作扩大实现或部署授权。
- 一项完整需求或缺陷使用一个主 issue 持续跟踪；开发、修复、CI 与合并复用它，不为每个操作、重试或补丁单独建 issue。跨仓库工作可在主 issue 关联各仓库 PR，只有可独立交付的问题才拆分 issue。
- PR 必须关联主 issue，使用 `Closes #<编号>`（跨仓库使用 `Closes <owner>/<repo>#<编号>`）在合并时自动关闭对应 issue，并将关键测试与 CI 结果简明留痕；不记录凭据、完整订阅链接或私有环境敏感信息。
- Issue 以代码/文档改动完成且测试与必需 CI 验证通过为完成标准，PR 合并即自动关闭；后续生产发布、线上部署与私有环境实测验收不绑定公开 GitHub Issue，另由本地运维/任务记录承接。
- 按 MVP 保持最小记录，不增加形式化审批、重复报告或额外测试要求；所有操作仍遵循用户授权和现有 CI、安全规范。

## Petauron 团队：任务工作区与失效引用收尾（MVP）

- 开发完成或交接时，按以下三步清理本任务不再需要的工作区、分支和失效远程跟踪引用；发布与验收继续按原任务跟踪，无需为等待后续阶段保留已无用途的工作区。
- 工作区：确认没有待保留的未提交内容或其他任务使用后，使用 `git worktree remove` 移除 worktree；受管工作区使用对应归档工具。独立检出先确认没有需保留的独有提交、stash、忽略文件及关联工作区，再删除。可重建的私有依赖和缓存可一起清理，不删除其他工作区引用的共享源。
- 任务分支：确认 PR 已合并到目标分支、本地分支没有超出 PR 合并时最终 head 的提交，且没有其他工作区或任务使用后，删除本地任务分支。Squash 合并以 PR 最终 head 为准；`git branch -d` 不适用时，核实上述条件后可用 `git branch -D`。已合并的远端任务分支按仓库自动删除设置清理；需手动删除时核对当前远端提交、使用情况及保护规则。
- 失效远程跟踪引用：核对远端和 refspec，先用 `git remote prune origin --dry-run` 预览；全部确认安全时执行 prune，混有不明或需保留项时仅定向清理已确认项，避免特殊 refspec 或 prune-tags 误删标签。普通已完成任务不逐条备份 reflog，也不为其新建 `refs/archive`。
- 未提交内容、独有资料、仍在使用的工作区、共享 Git 数据和受保护分支继续保留；只在有异常或依赖线索时进一步核查。需要保全独有历史时，可保留相应引用或使用可恢复的 Git 备份；仅导出引用文本不能替代备份。
- 存量归档引用与历史快照暂不清理，发布标签保留。本规则不自动授权批量整理历史工作区、修改生产、发布或创建定时任务。
