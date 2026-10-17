# `setSearchDomainForSystemdResolved` 改为记录 `resolvectl` 的失败，而不是丢弃

## 摘要

`(*NativeTun).setSearchDomainForSystemdResolved` 通过三条 `resolvectl` 命令配置 systemd-resolved。这三条命令的任何失败都被丢弃了，而且是**两次**：

1. 退出码被丢掉（`_ = ...Run()`）；
2. `resolvectl` 的 stderr 谁也看不到——`shell.Exec` 没有配 `Attach()`，子进程的 `Stderr` 保持为 nil。

随后该函数返回 `nil`，于是 `configure()` 判定成功。当这三条写入真的失败时，TUN 正常建起，调用方认为 DNS 已经交接完毕，而任何地方都没有留下"DNS 实际未被接管"的记录。结果就是整条 `dns-hijack` 链路无声失效，且没有任何诊断线索可查。

本 PR 保留结果而不是丢弃它，并通过 `t.options.Logger` 上报。刻意**只记录、不返回错误**——理由见 [为什么不返回错误](#为什么不返回错误)。

## 这个不对称早就被指出过

[PR #9](https://github.com/MetaCubeX/sing-tun/pull/9)（2026-02-13 合并）开篇即写：

> Previously, the initialization and teardown procedures were **asymmetrical**, causing
> DNS configurations to persist in `systemd-resolved` after the interface was …

那次 PR 新增了 `unsetSearchDomainForSystemdResolved`、接进 `Close`，**并在同一个提交里**给它加了一行 `t.options.Logger.Debug(...)`。但它没有动 `setSearchDomainForSystemdResolved`——那里三条 `_ = ...Run()` 至今原样保留，就在同一文件的隔壁函数里。

于是写入侧依旧不可观测，而拆除侧已经可观测了，"到底有没有配上 DNS？"这个问题只在一侧有答案。本 PR 补上另一半，同时也让姊妹模块 `SagerNet/sing-tun`（那边两个方向都不记录）与此处已有的这行 Debug 对齐。

## 根因

`tun_linux.go:1048-1063`：

```go
func (t *NativeTun) setSearchDomainForSystemdResolved() error {
	ctlPath, err := exec.LookPath("resolvectl")
	if err != nil {
		return nil
	}
	dnsServer, err := t.options.DNSServerAddress()
	if err != nil {
		return err
	}
	go func() {
		_ = shell.Exec(ctlPath, "domain", t.options.Name, "~.").Run()                                        // :1058
		_ = shell.Exec(ctlPath, "default-route", t.options.Name, "true").Run()                               // :1059
		_ = shell.Exec(ctlPath, append([]string{"dns", t.options.Name}, common.Map(dnsServer, netip.Addr.String)...)...).Run() // :1060
	}()
	return nil
}
```

**退出码被丢弃。** 三处调用全部 `_ =`。函数体内没有任何一处能观察到失败；又因为工作在游离 goroutine 中执行，即使事后检查，返回值也承载不了这个错误。

**诊断信息被丢弃。** 在 `github.com/metacubex/sing/common/shell` 中，`Exec` 只构造一个裸 `exec.Cmd`，而 `Attach` 才是设置 `Stdin`/`Stdout`/`Stderr` 的地方。此处从未调用它，因此 `Stderr` 保持 nil，`os/exec` 会把子进程的 stderr 接到 `/dev/null`。`resolvectl` **确实**打印了失败原因的解释——它被写进了 `/dev/null`。

**logger 从未被读取。** 该函数中没有任何地方读 `t.options.Logger`。

正确写法在本仓库内、同一个 `shell` 包上已有先例——`redirect_nftables_rules_openwrt.go:47-50`：

```go
output, err := shell.Exec(fw4Path, "reload").Read()
if err != nil {
	return E.Extend(E.Cause(err, "reload fw4 rules"), output)
}
```

改后这三条命令放进一个循环，做的正是这件事。

## 触发条件 A —— `systemd-resolved` 未运行

这是 Arch Linux 的出厂默认态：DNS 由 NetworkManager 接管，`systemd-resolved` 没有启用动机，于是停在 disabled。但 `resolvectl` **二进制**仍然装着，所以 `exec.LookPath` 成功、sing-tun 照常往下走——然后每一次 D-Bus 调用都在连接阶段失败。

在恰好处于该状态的宿主机上实测（`systemd-resolved` disabled、`/run/systemd/resolve/` 不存在、`/etc/resolv.conf` 是 NetworkManager 写的普通文件，指向 DHCP 下发的路由器地址）：

```
$ resolvectl status
Failed to connect to service /run/systemd/resolve/io.systemd.Resolve: No such file or directory
```

三条写入，附上被捕获的 stderr（即当前代码丢弃掉的内容）：

```
resolvectl domain Bettbox ~.                     exit status 1
    stderr: Failed to set domain configuration: ...
resolvectl default-route Bettbox true            exit status 1
    stderr: Failed to set default route configuration: ...
resolvectl dns Bettbox 198.18.0.2 fdfe:dcba:9876::2   exit status 1
    stderr: Failed to set DNS configuration: ...
```

而调用方看到的是：`nil` 错误，以及**没有任何日志输出**。

该触发条件是**永久性**的——重试永远不会好转——这使得"毫无提示"成为其中最昂贵的一种：之后不会再有症状，能反查到"DNS 配置丢失的那一刻"。

## 触发条件 B —— polkit 拒绝，且尚无认证 agent 注册

此情形下 `systemd-resolved` 正在运行且可达，D-Bus 调用能走到授权检查。四个写动作（`set-domains`、`set-dns-servers`、`set-default-route`、`revert`）在 `/usr/share/polkit-1/actions/org.freedesktop.resolve1.policy` 中被声明为 `auth_admin` / `auth_admin_keep`。`resolvectl` 默认**允许**交互式授权，因此它本身是能弹框的——但当客户端在登录时自启动，桌面认证 agent 尚未向 polkitd 注册，polkit 无处弹框。

已在容器中端到端复现（手动拉起 `dbus-daemon --system` + `polkitd` + `systemd-resolved`，无 agent，调用方非特权）：

```
caller: uid=1000 (unprivileged, so polkit is consulted)
precondition ok: systemd-resolved is reachable
```

先看对照组：同一容器内，同样三条命令以 root 执行——**全部成功**，因为 `sd_bus_query_sender_privilege()` 判定调用方具备特权，polkit 根本不会被咨询：

```
resolvectl domain Bettbox ~.                        -> Bettbox: Bus client set search domain list to: ~.
resolvectl default-route Bettbox true               -> Bettbox: Bus client set default route setting: yes
resolvectl dns Bettbox 198.18.0.2 fdfe:dcba:9876::2 -> Bettbox: Bus client set DNS server list to: 198.18.0.2, fdfe:dcba:9876::2
```

随后在无 agent 注册的情况下以非特权用户执行：

```
resolvectl domain Bettbox ~.
    exit: exit status 1
    stderr: Failed to set domain configuration: Access denied as the requested operation
            requires interactive authentication. However, interactive authentication has
            not been enabled by the calling program.
resolvectl default-route Bettbox true    -> 同上
resolvectl dns Bettbox 198.18.0.2 ...    -> 同上
```

最终状态——什么都没有配上：

```
$ resolvectl status Bettbox
Link 3 (Bettbox)
    Current Scopes: none
         Protocols: -DefaultRoute +LLMNR +mDNS -DNSOverTLS DNSSEC=no/unsupported
     Default Route: no
```

polkitd 自己的日志也是空的：它以 `--log-level=notice` 运行，只记录认证**失败**，因此一个"无从弹框"的请求同样不留痕迹。被丢弃的退出码 + 被丢弃的 stderr + 不存在的 polkit 记录，三者叠加，这次失败**彻底无痕**。

该触发条件是**瞬时且依赖策略**的，这让它更糟而不是更好：同一个二进制、同一台机器，一分钟后即成功，用户手上没有任何可用来对照反推的东西。

顺带一提，file capabilities 在这里帮不上忙——只有真正具备特权身份的调用方才能跳过 polkit——所以恰好是"带 `cap_net_admin` 运行"的配置会出问题，而 setuid 版本不会。

## 影响

DNS 接管完全依赖这条路径——它是把系统解析器交给 TUN 自身 DNS 服务器的唯一手段。一旦失败，本机应用解析到的是真实公网地址而非 TUN 的 DNS 服务器，于是：

- 域名型规则不再命中，流量静默走错出口；
- `hosts` 注入被绕过；
- 任何必须经代理才能取到 DNS 的场景直接失败，而报出来的错指向网络、指向不到 DNS 配置。

从应用视角看，"TUN 已开启"与"DNS 正被接管"变成了**无法区分**的两种状态。

## 改动内容

```diff
 	go func() {
-		_ = shell.Exec(ctlPath, "domain", t.options.Name, "~.").Run()
-		_ = shell.Exec(ctlPath, "default-route", t.options.Name, "true").Run()
-		_ = shell.Exec(ctlPath, append([]string{"dns", t.options.Name}, common.Map(dnsServer, netip.Addr.String)...)...).Run()
+		commands := [][]string{
+			{"domain", t.options.Name, "~."},
+			{"default-route", t.options.Name, "true"},
+			append([]string{"dns", t.options.Name}, common.Map(dnsServer, netip.Addr.String)...),
+		}
+		for _, args := range commands {
+			output, err := shell.Exec(ctlPath, args...).Read()
+			if err != nil && t.options.Logger != nil {
+				t.options.Logger.Error(E.Extend(E.Cause(err, "set DNS configuration for interface: ", t.options.Name), output))
+			}
+		}
 	}()
```

改动仅限一个函数，成功路径行为不变：同样三条命令、同样顺序、同样 argv，唯一区别是失败会被记录下来。

## 为什么不返回错误

很自然会想直接把失败向上传递。但那会造成回归：

`configure()`（`tun_linux.go:409-414`）在 `setSearchDomainForSystemdResolved` 返回非 nil 时会中止并拆掉 TUN。而本就不运行 `systemd-resolved` 的机器非常普遍（触发条件 A 就是 Arch 默认态），返回错误等于把"静默的部分失效"升级成"硬失效"，让那些压根没打算用 systemd-resolved 的用户直接失去隧道。

另外这段工作跑在游离 goroutine 中，不改成同步启动的话，返回值本来也承载不了结果。

**只记录**才是这个修复中安全的那一半，也正是此前缺失的那一半。

## 范围

本 PR 让失败**变得可观测**，但不会让 DNS 在任一触发条件下真正工作：

- 触发条件 A 需要为"resolved 缺席"提供兜底（自行接管 stub，或在调用方改配置形态——姊妹模块已经为此提供了一个可选服务，它会申请 `org.freedesktop.resolve1` 这个总线名）；
- 触发条件 B 无法在这里重试。polkit 那次失败能否清除取决于桌面 agent 何时注册，而这个进程既观察不到也查不到：polkit 的 `Authority` 接口只暴露 agent 侧方法，没有任何可供查询的东西。想要确定性的调用方，只能在隧道建起后轮询 `resolvectl status <if>` 检查 `DNS Domain: ~.`。

两者都属于调用方的职责，均不在本 PR 范围内。本 PR 在范围内的部分，是让命中这两者之一的用户能在日志里看到一行，而不是拿到一个看起来正常、却什么都解析不出来的隧道。

有一处可选的细化，为保持 diff 最小而**刻意未做**：消息可以区分"systemd-resolved 未运行"（用户自己选的配置，值得一行 Info）与"polkit 拒绝了请求"（瞬时故障，值得 Error）；对 `/run/systemd/resolve/io.systemd.Resolve` 做一次廉价的前置探测即可。

## 复现方式

两个彼此独立的触发条件，均已在本仓库版本上实测。复现工程在 `VillagerTom/sing-tun-resolved-repro`，其中 `sing-tun` 以 submodule 形式固定版本：

```
git clone --recurse-submodules https://github.com/VillagerTom/sing-tun-resolved-repro
cd sing-tun-resolved-repro

make test        # 两个触发条件，确定性，无需 root，无需容器
make issue531    # 触发条件 A，针对真实 resolvectl，在容器中
make issue522    # 触发条件 B，在容器中（需要 --privileged）
```

`make test` 会把 `tests/*.go` 复制进 submodule 以便访问未导出的函数，然后运行它们。测试通过在 `PATH` 上放置 `resolvectl` 替身，直接调用**真实的** `setSearchDomainForSystemdResolved`，并按序断言：

1. 触发条件确实以精确 argv 发生过——`domain Bettbox ~.`、`default-route Bettbox true`、`dns Bettbox 198.18.0.2 fdfe:dcba:9876::2`；
2. 命令确实失败了、报错信息确实存在——重放其中一条并捕获 stderr，得到的就是上文引用的原文；
3. logger 看到了东西。

今天失败的是第 3 条；由于第 1、2 条排在前面，它不可能空跑通过。**打上本 PR 后两条测试均通过**：

```
--- PASS: TestReproIssue522PolkitAgentNotReady (0.03s)
--- PASS: TestReproIssue531ResolvedAbsent (0.01s)
ok  	github.com/metacubex/sing-tun	0.048s
```

记录下来的正是此前被丢弃的那份信息：

```
error: set DNS configuration for interface: Bettbox: execute (/usr/bin/resolvectl) … :
       exit status 1: Failed to set domain configuration: Access denied as the requested
       operation requires interactive authentication. However, interactive authentication
       has not been enabled by the calling program.
```

补丁同时以 `patch/0001-…patch` 提供；`git apply` 之后重跑 `make test`，可见同样的由红转绿。

## `SagerNet/sing-tun` 中的同一缺陷

姊妹模块有完全相同的三条 `_ = ...Run()`、完全相同的游离 goroutine，其 `github.com/sagernet/sing/common/shell` 与此逐字节相同，且两个方向都不记录日志。sing-box 消费的是那个模块，因此 sing-box 用户同样会命中这两个触发条件；在此处的修复不会自动传播过去，那个模块需要单独处理。

## 下游报告

暴露该缺陷的两个现场报告，都是通过以 mihomo core 为基础的 GUI 客户端进来的，也就是通过本模块：

- https://github.com/appshubcc/Bettbox/issues/531 —— `systemd-resolved` 未运行（Arch 默认态），三条写入全部失败，DNS 接管静默缺失。
- https://github.com/appshubcc/Bettbox/issues/522 —— polkit 因认证 agent 尚未注册而拒绝写入，结果相同；且同样三条命令在一分钟后即成功。

## 提交方式说明

本仓库的 issue 功能已关闭，因此以 pull request 而非 issue 的形式提交。