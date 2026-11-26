# go-cacheloader

Go 的只读快照缓存组件。业务提供数据源读取函数；组件负责缓存命中、故障回源、负缓存、访问触发刷新和并发控制。
源服务/DB 是事实来源。支持普通 JSON 值及有序集合快照，不提供业务写入或任务消费接口。

## 快速接入

```go
import (
    "context"
    "time"

    "github.com/Luoyuhao/go-cacheloader/v2"
    "github.com/Luoyuhao/go-cacheloader/v2/memory"
    "github.com/Luoyuhao/go-cacheloader/v2/string_cacheloader"
)

// 示例业务类型可以包含 slice/map，不需要 comparable。
type User struct { Name string; Tags []string }

func example(ctx context.Context) error {
    backend, err := memory.New(10000)
    if err != nil { return err }
    users, err := string_cacheloader.New(backend,
        func(ctx context.Context, key string) (User, bool, error) {
            // 实际接入改为 repo.Find(ctx, key)。不存在返回 found=false。
            return User{Name: "Alice", Tags: []string{"reader"}}, true, nil
        }, cacheloader.Config{
            TTL: 5*time.Minute,
            NegativeTTL: time.Second,
            RefreshAfter: 10*time.Second,
        })
    if err != nil { return err }
    defer users.Close()
    user, found, err := users.Get(ctx, "users:v1:42")
    _ = user
    _ = found
    return err
}
```

同一实例的同 key 回源合并；源并发在启动任务前限额。缓存读写故障不妨碍返回成功的源读取。
刷新默认在后台执行。设置 `RefreshAfter: 30 * time.Second, SyncRefresh: true` 后，满 30 秒的快照命中会等待回源。成功返回新值，失败返回本次读到的旧快照。
底层 `TTL` 应长于刷新间隔以保留兜底快照，也可设为 `0` 让普通快照不过期。成功刷新并写入后重置 TTL。
每次读取返回独立 JSON 解码结果；缓存类型/结构升级时使用新的 key 命名空间。

## 可选批量接入

普通 `New(backend, load, cfg)` 仍然有效。需要批量回源时增加一个选项：

```go
users, err := string_cacheloader.New(backend, repo.LoadUser, cfg,
    string_cacheloader.WithBatchLoader(repo.LoadUsers))
// LoadUsers 签名：func(context.Context, []string) ([]cacheloader.Result[User], error)
// 每个 key 对应一个按输入顺序排列的结果；不存在的位置使用 Found=false。
```

后端额外实现 `BatchStringCacher.MGet(ctx, keys...) ([]Result[string], error)` 后，组件自动批量读缓存。
缓存和源的批量能力可以独立启用；未提供的一侧回退到单 key 调用。memory 后端已支持批量读取。
默认 BatchSize=100，并受 MaxConcurrency 限制；一次 MGet 只计一次请求限流。
缓存批量响应失败或长度不符时丢弃该响应并回源。源批量失败时，冷读返回错误，同步刷新使用原快照。
回填仍逐 key CAS，并非多 key 写入事务。详细超时和并发语义见 [string 契约](docs/string.md)。

## 功能与边界

| 能力 | 接口/行为 |
| --- | --- |
| 单值读取 | `Get(ctx, key) (T, found, error)` |
| 批量读取 | `MGet(ctx, keys...) ([]Result[T], error)`，保序、去重、分批，支持可选缓存/源批量 IO |
| 有序快照 | `zset_cacheloader.New`，提供 Get / ZRange / ZCard |
| 资源回收 | `Close()` 停止接收新读取，取消并等待在途回源/刷新 |
| 内存后端 | `memory.New(capacity)`，带 LRU、TTL、原子条件替换 |
| 自定义后端 | 实现 StringCacher 的 Get / CompareAndSwap；两种 loader 共用 |

v2 的模块路径是 `github.com/Luoyuhao/go-cacheloader/v2`。从 v1 迁移时需要更新 import 路径和调用方式。
v2 移除了 Builder、StringLoader/Loads、Locker、全局限流配置及 zset 增删接口。
采用 `LoadFunc[T] + Config`；旧缓存原始 JSON/哨兵不兼容新 envelope，迁移时更换 key 前缀。
Redis 适配器尚未随库提供；适配器必须原子完成值比较、写入和 TTL 设置，不能用独立 GET + SET 模拟。

默认源超时 2s、每次缓存操作超时 200ms、每实例并发 500；可用 RequestsPerSecond 配置实例请求限流。回调必须响应 context；无法强制终止忽略取消的外部代码。
只保证最终一致，不保证强一致、跨 key 快照一致、DB 事务或 exactly-once。TTL=0 且关闭刷新时不会自动跟随源变化。

## 文档

- [string 访问契约](docs/string.md)
- [zset 快照契约](docs/zset.md)
- [接入设计与迁移](docs/api-design.md)
- [决策记录](docs/decisions.md)
- [Agent 开发入口](AGENTS.md) / [代码地图](.agents/code-map/README.md)

## 验证

```sh
go test ./...
go test -race ./...
go vet ./...
```
