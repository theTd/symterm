# symterm 同步性能优化指南

## 当前瓶颈分析

以 symterm 项目（22,173 文件）跨洋同步到 crystal.thetd.me 为例：

| 指标 | 数值 |
|------|------|
| 网络 RTT | ~228ms |
| 总同步时间 | 236s |
| Upload-files 阶段 | ~210s |
| Manifest batches | 44 |
| Upload bundles | 40 |
| 总 RPC 数 | 335 |

**核心问题**：server 端 `Serve` 循环是单线程串行处理请求。228ms RTT 下，任何同步阻塞 RPC 都会积累大量等待时间。

### 时间分布估算

```
Manifest batches (44 × RTT)          ~10s  ← 已优化
Upload bundles begin (40 × RTT)      ~9s   ← 可优化
Bundle payload 传输+server 写入      ~180s ← 主要瓶颈
其他 RPC + 进度报告                   ~15s
```

Bundle commit 耗时长的原因是 server 端串行执行：对每个文件解压 → hash 校验 → `os.WriteFile` → 更新 manifest。40 个 bundle 排队执行，磁盘 I/O 无法并行。

---

## 已完成的改动（客户端）

### 1. 并发发送 Manifest Batches

**文件**：`internal/sync/initial_sync_session.go:sendManifestBatches()`

将串行循环改为 goroutine 并发发送所有 batch。server 端 `SyncManifestBatch` 只是写内存 map（有 `m.mu` 保护），无顺序依赖，并发安全。

**预期收益**：~10s

### 2. 调大 Bundle 参数

**文件**：`internal/sync/initial_sync_session.go`

| 参数 | 旧值 | 新值 |
|------|------|------|
| `syncBundleTargetBytes` | 32MB | 64MB |
| `syncBundleMaxFiles` | 512 | 1024 |

**预期收益**：bundle 数量从 ~40 降到 ~20，减少 ~20 个 begin RPC，省 ~4-5s

---

## 下一步优化（server 端）

真正的数量级提升需要在 server 端实现**请求并发处理**。当前 `internal/transport/server_codec.go:Serve()` 是单线程顺序处理：

```go
for {
    line, _ := s.reader.ReadBytes('\n')     // 读请求
    response, _ := s.dispatch(serveCtx, req) // 同步处理
    s.writeResponse(response)                // 写响应
}
```

### 方案 A：对安全的方法启用 goroutine dispatch（推荐）

对无因果依赖且内部有锁保护的方法，启动 goroutine 异步处理。

**改动点**：`internal/transport/server_codec.go`

```go
// 在 dispatchRoutes 中增加 async 标记
type dispatchRoute struct {
    invoke func(context.Context, Request) (Response, string)
    async  bool
}

// 在 Serve 循环中
if route, ok := s.dispatchRoutes[request.Method]; ok && route.async {
    s.streamWG.Add(1)
    go func(req Request) {
        defer s.streamWG.Done()
        response, _ := route.invoke(serveCtx, req)
        s.writeResponse(response)  // writeMu 已保护
    }(request)
    continue
}
```

**可安全标记为 async 的方法**：

| 方法 | 原因 |
|------|------|
| `sync_manifest_batch` | 只写内存 map，`m.mu` 保护；batch 之间无顺序依赖 |
| `upload_bundle_commit` | I/O 期间释放 `m.mu`，可并行做磁盘写入 |

**⚠️ 注意**：`upload_bundle_commit` 依赖 `upload_bundle_begin` 创建的 BundleID。但 client 是等 begin 响应回来后才发 commit，且 server 是顺序读取请求的，所以 commit 一定在 begin 之后被读取。goroutine 调度可能让 commit 先于 begin 执行，需要确保 begin 不是 async 的，或者加入 happens-before 机制。

**更简单的方式**：只把 `sync_manifest_batch` 标为 async（零风险），`upload_bundle_commit` 保持同步。

### 方案 B：WorkspaceManager 内部队列化 Bundle Commit

不改 transport 层，而是在 `WorkspaceManager.UploadBundleCommit` 内部做异步落盘：

1. `UploadBundleCommit` 收到请求后，快速校验并返回成功
2. 将文件写入任务提交到内部 worker pool（如 4 个 goroutine）
3. `FinalizeSync` 等待所有 pending 写入完成

**风险**：改变语义——commit 返回成功时文件可能还未落盘。如果 finalize 前进程崩溃，可能丢数据。

### 方案 C：Client 端 Pipeline 发送 Bundles

不改 server 端，在 client 端预读+流水线发送：

```go
// 1. 并发 begin 所有 bundles（server 串行但处理快，省 RTT）
// 2. 逐个 commit，但在 commit bundle N 的同时预读 bundle N+1 到内存
```

**预期收益**：有限，因为 server 处理时间 dominates，预读只能隐藏本地 I/O。

---

## 收益预估

| 优化 | 预期节省 | 风险 |
|------|----------|------|
| 并发 manifest batches（已做） | ~10s | 低 |
| 调大 bundle 参数（已做） | ~5s | 低 |
| Server async manifest batch | ~10s | 极低 |
| Server async bundle commit | ~60-100s | 中（需保证 begin/commit 顺序）|
| 并发 bundle begin（client） | ~5s | 低 |

如果实现 server 端 async bundle commit，配合 client 端并发 begin，**有望把 236s 压到 120-150s**。

---

## 测试建议

每次改动后建议跑：

```bash
# 全项目编译
go build ./...

# 核心包测试
go test ./internal/sync/... ./internal/transport/... ./internal/daemon/... -count=1

# 实机测试（到远程 server）
# 观察 sync trace 中的 rpc_count 和 upload_bundles
```
