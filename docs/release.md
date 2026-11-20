# 发布流程

版本号用日期，形如 `2026.11.12`；同一天要再发一版就加后缀，如 `2026.11.12-rc.1`。`sprout --version` 打印的就是这串，和 git 标签一致（标签写 `v2026.11.12`，日期版本号本身不带 `v`）。

## 一条命令

```bash
# Windows
release.cmd

# Linux / macOS
go run ./tools/release
```

默认版本号是当天日期。指定版本号：

```bash
release.cmd 2026.11.12
release.cmd 2026.11.12-rc.1
```

工具会依次做：

1. `go build ./...`、`go vet ./...`、`go test ./...`，任何一步失败就停下，不会产出半成品。（`-check=false` 可跳过，只在你已经单独验证过时用。）
2. 交叉编译八个目标，`CGO_ENABLED=0`、`-trimpath`、`-ldflags "-s -w -X main.version=<版本号>"`。Termux 目标用 `GOOS=android`，不能拿 `linux/arm64` 的产物代替（原因见下方「Termux 必须单独编译」）。
3. 每个目标打一个包：Windows 用 `.zip`，其余用 `.tar.gz`；包里是顶层目录 `<包名>/`，装着可执行文件和 `README.md`。
4. 写 `SHA256SUMS.txt` 和 `RELEASE.md`。

产物在仓库根的 `release/<版本号>/`，已进 `.gitignore`，不随仓库提交。

## 目标平台

| 平台 | 文件 |
| --- | --- |
| Windows x64 | `sprout_<版本>_windows_amd64.zip` |
| Windows ARM64 | `sprout_<版本>_windows_arm64.zip` |
| Linux x64 | `sprout_<版本>_linux_amd64.tar.gz` |
| Linux ARM64 | `sprout_<版本>_linux_arm64.tar.gz` |
| Android Termux（ARM64） | `sprout_<版本>_termux_arm64.tar.gz` |
| Linux ARMv7 | `sprout_<版本>_linux_armv7.tar.gz` |
| macOS Intel | `sprout_<版本>_darwin_amd64.tar.gz` |
| macOS Apple 芯片 | `sprout_<版本>_darwin_arm64.tar.gz` |

- **在 target 列表里增删平台**：改 `tools/release/main.go` 的 `targets()`；发布说明里的下载表由它生成，不会出现「写了但没打这个包」。

### Termux 必须单独编译

Termux 包必须是 `GOOS=android GOARCH=arm64`，**不能**用 `GOOS=linux` 的 arm64 产物代替。曾经这样发过一版，在真机上启动即崩：

```text
SIGSYS: bad system call
PC=0x23690 m=0 sigcode=1
syscall.Syscall6(0x1b7, ...)   ← 0x1b7 = 439 = faccessat2
internal/syscall/unix.Eaccess
os/exec.findExecutable
github.com/atotto/clipboard.init.0
```

Android 的 seccomp 策略禁止 `faccessat2` 系统调用。Go 的 `syscall.Faccessat` 在 `flags != 0` 时会**先试** `faccessat2`，期望拿到 `ENOSYS` 再回退；但被 seccomp 拦下时进程直接收到 `SIGSYS`，没有回退的机会。Go 源码里的处理是「`runtime.GOOS != "android"` 才尝试」，所以 android 版 runtime 根本不会发这个调用，`internal/syscall/unix.Eaccess` 也直接返回 `ENOSYS` 让调用方按权限位判断。

这个崩溃发生在任何 `exec.LookPath` 上，`atotto/clipboard` 的 `init` 只是最早的调用者——换掉这个库只会把崩溃推迟。`CGO_ENABLED=0` 下 android 目标产物是不依赖 bionic 的静态 ELF，Termux 里可以直接跑。`tools/release` 里有测试钉住这条约束（`TestTermuxTargetUsesAndroidGOOS`）。

## 发布说明

正文来自 `docs/releases/<版本号>.md`，这个文件随仓库提交。`RELEASE.md` = 它 + 自动生成的下载表、校验方法、源码构建说明。

文件不存在时工具会生成一份草稿再继续跑完，所以不会卡住；但草稿里的「待补充」必须补齐再发。看控制台有没有这行提示：

```text
提示: 发布说明还不存在，已生成草稿 docs/releases/2026.11.12.md，请补充后重新运行
```

改完重新跑一遍即可，压缩包内容不变（除时间戳），所以重复执行是安全的。

## 发布前检查

- [ ] `docs/releases/<版本号>.md` 已补全，尤其是「已知限制」——没做的事要写明白，不要让人踩了才知道。
- [ ] 手工跑一次新打出来的包：`sprout --version` 显示正确版本，TUI 能起、能对话。
- [ ] 确认没把凭证打进包：压缩包里只有可执行文件和 `README.md`，数据目录不在其中。
- [ ] 打注释标签并推送：`git tag -a v2026.11.12 -m "Sprout 2026.11.12"`，再 `git push origin main v2026.11.12`。直连 github.com 超时时，给 git 加 `-c http.proxy=<代理地址> -c https.proxy=<代理地址>`；TLS 握手偶发失败时重试一次即可。
- [ ] 上传 Release：标签推上去之后，用下方「已知不足」里的 `gh release create` 命令把压缩包和 `SHA256SUMS.txt` 传上去，否则 `sprout update` 查不到新版本。

## 已知不足

- **macOS 包未签名、未公证**。用户首次运行需要右键 →「打开」，或执行 `xattr -d com.apple.quarantine sprout`。要彻底解决需要 Apple 开发者账号。
- **CI 只验证能编译和打包**，不上传产物。`.github/workflows/ci.yml` 在 Windows / Linux / macOS 上跑 build / vet / test，另一个 job 在 Linux 上把八个包打一遍，确认交叉编译管线没断。
- **`tools/release` 不负责上传**。产物在本地 `release/<版本号>/`，上传要另外执行（`gh` 需已登录且有 `repo` 权限，标签须已推送，`--verify-tag` 会校验这一点）：

  ```bash
  gh release create v2026.11.12 release/2026.11.12/sprout_2026.11.12_*.zip release/2026.11.12/sprout_2026.11.12_*.tar.gz release/2026.11.12/SHA256SUMS.txt --repo shumengya/sprout --verify-tag --title "Sprout 2026.11.12" --notes-file release/2026.11.12/RELEASE.md
  ```

  传完用 `gh release view v2026.11.12 --repo shumengya/sprout --json assets` 核对附件是否齐全。
- **`sprout update` 依赖发布附件齐全**。它读 GitHub Releases 的最新发布（tag 与 `sprout --version` 同一串，可带 `v`），有新版本时下载本平台压缩包和 `SHA256SUMS.txt`，校验、解压、用 `--version` 试运行后替换当前程序（`--check` 只报告）。所以：tag 与产物文件名必须和 `tools/release` 生成的一致，否则本地版本永远对不上；`SHA256SUMS.txt` 必须一起上传，缺了它自动更新会直接拒绝；压缩包顶层目录里的程序名必须是 `sprout` / `sprout.exe`。
- **Linux / macOS / Termux 只验证到能编译**：打包流程本身与平台无关，但未在真实系统上双击 / 解压运行过。
