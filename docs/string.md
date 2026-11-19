# string 只读快照访问契约

业务通过 Get/MGet 读取 DB/源服务的副本，组件决定何时回源。更新业务数据在源端完成。
当前接口为 `string_cacheloader.New[T any](backend, load, Config)`，不需要 Builder、Loader 类或 Locker。可用 WithBatchLoader 额外注册批量源读取。

## 类型与结果

```go
load := func(ctx context.Context, key string) (User, bool, error) {
    return repo.FindUser(ctx, key)
}
cl, err := string_cacheloader.New(backend, load, cacheloader.Config{
    TTL: 5*time.Minute, NegativeTTL: time.Second, RefreshAfter: 10*time.Second,
})
// 检查 err 后使用；应用退出时 Close。
user, found, err := cl.Get(ctx, "users:v2:42")
items, err := cl.MGet(ctx, "users:v2:42", "users:v2:43", "users:v2:42")
```

- T 可以包含 slice/map，必须能够用标准 encoding/json 正确往返；不支持 channel/function、非有限浮点数等值。
- 源返回 found=false 时忽略 value，缓存不存在状态，返回 T 零值；源错误不会被负缓存。
- found=true 可以配合法的 0、空字符串甚至 nil；不存在与零值不再混淆。
- 源值在回调返回后直至编码完成期间不能被其他线程修改。调用方拿到的是解码副本，可独立修改。
- 同 key 并发读取、重复 MGet 位置也分别解码，不向多个调用者共享可变源指针。
- 自定义 JSON 编解码必须支持一致的往返语义，不应产生全局副作用或耗时阻塞。

## 可选批量能力

```go
cl, err := string_cacheloader.New(backend, load, cfg,
    string_cacheloader.WithBatchLoader(func(ctx context.Context, keys []string) ([]cacheloader.Result[User], error) {
        // 示例：一次 IN 查询得到按 key 索引的 map，再还原输入顺序。
        byKey, err := repo.FindUsers(ctx, keys)
        if err != nil { return nil, err }
        out := make([]cacheloader.Result[User], len(keys))
        for i, key := range keys {
            value, found := byKey[key]
            out[i] = cacheloader.Result[User]{Value: value, Found: found}
        }
        return out, nil
    }))
```

缓存后端可选实现 `BatchStringCacher`：

```go
MGet(ctx context.Context, keys ...string) ([]cacheloader.Result[string], error)
```

返回的 Value 是未解码的 envelope，Found=false 表示缺失/过期；Found=true 且内容损坏会触发回源。
缓存批量与源批量可分别启用；没有批量能力的一侧使用单 key 回调。
Get 及由 Get 触发的单 key 刷新仍使用单 key 源函数，必须保证两种源函数语义一致。

MGet 按输入顺序返回结果，先全局去重再分批，重复位置分别解码为独立值。
每批最多 min(BatchSize, MaxConcurrency) 个不同 key；默认 BatchSize=100。批次顺序执行。
初次读取和取得回源额度后的缓存复查都使用可选批量缓存接口，冷批次通常最多两次批量读缓存。
只有仍然缺失/损坏且由本次任务取得执行权的 key 进入批量源函数；已有在途 Get/MGet 的 key 直接等待共享结果。
命中快照达到刷新间隔时也可按批刷新；后台刷新在所有前台批次完成后才准入，
避免前批命中的刷新抢占后批 miss 所需额度。刷新准入失败不影响命中结果。

缓存返回 error 时丢弃所有伴随结果；成功却长度不等也作为缓存错误回源。
源批量结果必须等长且按序，不存在位置填 Found=false，不能省略；批量 error/panic/长度错误导致该源批次失败。
使用批量源函数时，先验证全部源结果的 JSON 编码与解码，再回填缓存。批量源失败直接返回错误，不触发单 key 重试。
未配置批量源函数时，逐 key 回源并按 key 交付结果。单个 key 的回源或编码失败只影响该 key 的等待者。
同组其他成功结果仍可回填，并交付给等待相同 key 的 Get。对外 MGet 任一项失败时返回整体错误，结果为 nil。
失败的 key 不写负缓存，已完成的回填也不回滚。

MaxConcurrency 继续按**在途 key 数**计算，不能用一批 100 个 key 只占一个额度。
一批新 miss 的额度不足时原子拒绝这一组，返回 ErrDowngraded，不启动其中一部分，也不等待可能形成循环依赖的额度。
调用者取消不取消共享任务，Close 会取消并等待整个批次。MGet 无论多少批均只计一次 RequestsPerSecond。
空参数返回空 slice；取消/已 Close 时仍返回对应错误。

每批缓存读取、复查、回填各有独立 CacheTimeout；每批源阶段共享一个 LoadTimeout。
单 key 回退也共享该批阶段预算，因此应按查询延迟调整 BatchSize/超时；不要假设 100 个串行回调各有 2s。
回填逐 key 执行 CAS，整个回填阶段共享 CacheTimeout，超时后剩余回填跳过但仍返回成功源结果。
暂未提供批量 CAS。MGet 不承诺源或缓存的跨 key 一致性，不是 DB 事务。

## 读取与错误

| 场景 | 行为 |
| --- | --- |
| 合法缓存命中 | 返回快照，达到 RefreshAfter 时尝试后台刷新 |
| 缺失/过期 | 同步等待源读取和一次有超时的回填尝试 |
| 缓存读取失败、超时或 panic | 记录错误，尝试源读取 |
| 缓存 JSON 损坏或版本/元数据非法 | 记录错误，回源后尝试条件修复 |
| 源错误、源超时、源 panic、JSON 编码/往返失败 | 返回错误，不写入新快照 |
| 源成功、缓存写入失败/超时/panic | 记录错误，仍返回成功源结果 |
| CAS 不匹配 | 放弃写入，仍返回本次源结果；不覆盖别的读取已经安装的快照 |
| 刷新失败 | 保留原快照；以后访问可以再次尝试 |
| 请求速率超限 | Get/MGet 返回 ErrDowngraded，命中也不放行；每个 MGet 计一次 |
| 源并发额度已满 | 冷读返回 ErrDowngraded；缓存命中仍返回旧值并跳过此次刷新 |
| 调用者取消 | 该调用及时返回 context 错误，不取消其他等待者共用的源读取 |
| Close 后读取 | 返回 ErrClosed |

不存在由 found=false 表达，不通过公开 Get 的 ErrNotFound 表达。
OnError 默认使用 slog；自定义回调可并发调用，应立即返回且不能回调本实例 Close。观察回调 panic 会被记录，不退出进程。
组件不会创建一个 goroutine 来强行中断不遵守 context 的后端/源回调，因此超时依赖这些实现配合。

## TTL 与刷新

| 配置 | 零值/默认值 |
| --- | --- |
| TTL | 0 = 普通存在值不过期 |
| NegativeTTL | 0 使用 1s；必须非负，不能用 0 禁用负缓存 |
| RefreshAfter | 0 关闭访问触发刷新 |
| LoadTimeout | 0 使用 2s，独立约束源回调 |
| CacheTimeout | 0 使用 200ms，分别约束缓存读和条件写 |
| MaxConcurrency | 0 使用 500，每实例源读取/刷新按在途 key 数共享限额 |
| BatchSize | 0 使用 100，限制 MGet 每批不同 key 数；实际还受 MaxConcurrency 限制 |
| RequestsPerSecond | 0 关闭请求限流；正值按实例的一秒窗口限制公开 Get/MGet 次数 |

负时长/负并发构造时报错。刷新间隔可以长于 TTL，此时主要依靠过期回源。

**成功写入或刷新会重置 TTL 为配置值**，保留通过访问刷新延长有效期的意图：
存在值使用 TTL；不存在值使用 NegativeTTL。TTL=0 的快照仍可刷新；不再查询后端 TTL，
不混用 Redis 的 -1/-2/0 特殊状态。

仅命中且达到快照 WrittenAt + RefreshAfter 时刷新，无后台轮询。WrittenAt 在源成功编码前记录；
缓存写耗时计入快照年龄。跨实例采用快照内时间判断，需要通常的时钟同步。
热 key 可持续被续期；TTL=0 + RefreshAfter=0 则始终保留副本直至后端淘汰，不能期待自动更新。
失败不会写入成功刷新时间；持续错误下仍可能频繁尝试，当前未提供熔断/退避。

## 原子快照后端

必需的 StringCacher 只有两项能力；批量读取是独立可选接口：

- `Get(ctx, key)`：返回组件写入的原始快照串；不存在/过期返回 ErrNotFound（支持 errors.Is）。
- `CompareAndSwap(ctx, key, previous, value, ttl)`：原子检查并写入完整值及 TTL。
  previous=nil 仅匹配不存在/过期；非 nil 要与当前仍存活的完整串相等。
  不匹配返回 false, nil；TTL=0 表示永久。过期 key 绝不能匹配旧值。

组件 envelope 含版本、每次生成的随机 revision、写入时间、Found 与 Value。
`found` 必须是布尔值，`value` 字段必须存在。缺失字段按损坏缓存回源，显式 `value: null` 可表示支持 nil 的值。
同值重新写入也有新 revision，避免“内容相同但已换过一代”被误匹配。
冷读观察到不存在后不能覆盖并发安装的新值；刷新观察到旧值后不能覆盖新快照或复活过期 key。
缓存本身失败时只以 previous=nil 尝试回填，绝不盲目覆盖未知值。

Redis 实现应在单个 Lua 脚本/等效原子机制中比较 GET 结果并 SET（TTL>0 时 PX，TTL=0 时不带过期参数）。
不能在客户端 GET 后独立 SET。时间精度不足时正 TTL 应向上取整，不能变成永久。
本库提供 memory 后端，Redis 生产适配器未提供；接入者需自行验证其原子性。

## 并发、生命周期和一致性

同一实例同 key 的 Get/MGet miss/refresh 共用在途状态；取得额度后还会以 CacheTimeout 再读一次缓存，
避免延迟到达的 miss 观察在另一任务回填完成后重复回源。不同实例通过 CAS 防覆盖，但不合并 DB 查询。
以在途 key 数限额，在创建 goroutine 前准入；不再维护有竞态的刷新 LRU 或先创建 goroutine 再等令牌。
缓存读本身不在源并发额度内，入口请求并发仍需服务层控制。

源任务保留首次触发者的 context 值，去除其取消和 deadline，并附独立 LoadTimeout/关闭取消；
各调用者自行受原请求 deadline 约束。同 key 的回源逻辑必须只依赖 key，不能依赖不同请求的隐藏租户/权限状态。
租户、权限视图和数据类型等影响结果的条件必须编码进 key。
源阶段完成后同步编码、再以独立 CacheTimeout 尝试回填；JSON CPU 时间不被强制抢占。
取消全部调用者时已经准入的源任务仍可运行到超时并填缓存。

Close 拒绝新读、取消源任务并等待全部已准入任务退出；同时通知在途缓存 Get 取消，但不等待外部调用者 goroutine。
后端/源函数不响应取消或 OnError 阻塞会延长关闭时间。构造函数不启动后台任务。

CAS 保护缓存写入代次，不是 DB 版本/事务：一次源读取仍可能读取副本延迟或陈旧数据，
也不保证调用者收到的值总是比其他调用者新。持续访问且源/缓存恢复后，靠刷新/过期最终收敛。
