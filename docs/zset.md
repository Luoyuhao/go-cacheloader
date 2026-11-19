# zset 只读快照契约

zset_cacheloader 用于排行榜、有序 ID 集合等完整快照读取，独立于调度/任务消费场景。
DB/源服务是事实来源；业务更新源数据，组件经刷新/过期收敛。
不暴露 ZAdd、ZPop、ZRem，不提供业务写穿、持久消费或队列领取语义。

## 接入

```go
cl, err := zset_cacheloader.New(backend,
    func(ctx context.Context, key string) ([]cacheloader.Z, error) {
        // 必须加载完整集合，不是前一页；Member 使用稳定 ID。
        return repo.LoadRanking(ctx, key)
    }, cacheloader.Config{
        TTL: 5*time.Minute, NegativeTTL: time.Second,
        RefreshAfter: 10*time.Second,
    })
// 检查 err；应用退出时 cl.Close()。
all, err := cl.Get(ctx, "ranking:v1:daily")
top10, err := cl.ZRange(ctx, "ranking:v1:daily", 0, 9, true)
count, err := cl.ZCard(ctx, "ranking:v1:daily")
```

backend 与 string loader 相同，实现 Get/CompareAndSwap。
当前将整个规范化集合编码成一个 string 快照，范围和数量在本地计算；不使用原生 Redis zset。
因此不需要空集合 marker、多 key 事务或业务自写集合替换脚本。
生产可接 Redis 原子 string 后端，当前随库提供的是有容量上限的 memory 后端。

## 快照规则

- Member 为稳定且合法的 UTF-8 字符串身份，同一身份出现多次时最后一个 score 生效。
- Score 为有限 float64；NaN、正负无穷均报错，避免 JSON 无法表示或排序含糊。
- 默认按 score 升序，同分按 Member 的字节字典序升序；reverse 同时反转这两层顺序。
- 源的 slice 不会被原地排序/修改；返回结果也是独立副本。
- 源 nil/空 slice 是合法空集合，使用 NegativeTTL 缓存；Get 返回非 nil 空 slice，ZCard 返回 0。
- “集合为空”与“缓存未命中”不同，空集合不会每次读取都回源。
- ZRange start/stop 为含端点 rank，负数从末尾计数；越界截断、无交集或 start>stop 返回空 slice。

Get、ZRange、ZCard 都通过同一完整快照读取机制。
一次操作内范围/数量对应单一快照；分开的多次调用可能跨过刷新，不保证共享版本。
如果需要同一时刻的数量和范围，调用一次 Get 并自行计算。

## 回源、刷新与错误

与 [string 契约](string.md) 共用超时、并发、同 key 合并、CAS、Close 和错误观察机制：

| 场景 | 行为 |
| --- | --- |
| 命中非空/空快照 | 返回当前结果；达到 RefreshAfter 后可异步刷新 |
| 未命中、缓存读失败/损坏 | 加载完整源集合并规范化，尝试条件回填；缓存失败仍能返回源结果 |
| 刷新成功 | 原子替换整个快照，源中删除的成员不残留；重置配置 TTL |
| 非空变空 | 安装完整空状态并使用 NegativeTTL |
| 空变非空 | 安装完整非空状态并使用普通 TTL |
| 源错误、非法 score、非法 UTF-8 成员 | 返回错误（后台刷新则保留旧快照并报告），不回填该快照 |
| 刷新期间原 key 过期或被另一写入替换 | CAS 拒绝写入，不能复活或覆盖新代次 |

TTL=0 表示普通非空快照永久有效，仍允许访问触发刷新。刷新并不维持原剩余 TTL，而是成功后续期。
后台刷新是只读源调用，不执行任何业务消费/修改。

## 适用规模与限制

范围查询也会读取/解码完整快照，缓存命中成本与集合大小成正比，源回填还需 O(n log n) 排序。
适合有明确大小上限的完整集合；不适合巨大队列、无限集合或要求服务端增量排名的场景。
没有原生 zset 的网络按页读取、score 范围过滤、rank 查询及集合运算能力。
需要这些能力时应另行设计只读范围协议与一致性模型，不把部分页伪装成完整快照。
集合 key 不得与普通 string loader 或不同 schema 混用；结构变化时换 key 前缀。
