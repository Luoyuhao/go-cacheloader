# 接入设计与迁移

当前已落地 `New(backend, LoadFunc[T], Config)`，替代 Builder + Cacher/Loader/Locker 多项业务注册。
string 与 zset 共享快照引擎和配置；业务只读，缓存写是组件内部行为。

| 旧设计 | 当前设计 |
| --- | --- |
| comparable、any 回源 | T any；带类型的 LoadFunc |
| nil/零值混用 | Get 返回 value/found/error，负缓存保留 Found |
| Loader 强制 Load/Loads | 单 key 函数必需；WithBatchLoader 可选，缓存批量能力自动识别 |
| 必须实现 Locker | 删除；组件本地合并，后端 CAS 保护缓存写入代次 |
| TTL 查询、特殊值与 Set | 原子 CompareAndSwap，同时替换整个快照并重置 TTL |
| LRU 刷新时间与后台全局令牌 | 快照 WrittenAt 与每实例在途任务表，启动 goroutine 前限额 |
| 导出的 SetCache/BaseCacheLoader | 私有 reader；业务只看 Get/MGet/Close |
| ZAdd/ZPop | 删除；有序完整快照 Get/ZRange/ZCard |
| 全局限流和默认并发 | Config.MaxConcurrency 控制实例回源/刷新，RequestsPerSecond 控制实例请求限流 |

缓存数据格式不兼容旧原始 JSON/负缓存哨兵。推荐更换 key 前缀；旧后端应改成真正原子的 CAS。
MGet 已支持可选 BatchStringCacher 与 WithBatchLoader；旧 Loads 可迁移为返回逐 key Result 的批量函数。
未启用的一侧自动使用单 key 调用。批量按序、失败回源、同 key 合并和并发预算保持一致；
BatchSize 控制请求大小，批量源/缓存阶段各共享对应超时，回填仍逐 key CAS。

已修复的关键问题：MGet 故障伪零值、损坏缓存不回源、无过期值无法刷新、LRU 共享锁修改、
后台 goroutine 无界排队、回源无内置超时、无关闭接口、300ms 锁租期不足、后台 panic 退出进程。
CAS 不替代源数据版本；最终一致的实际边界见 string 契约。

暂未实现：Redis 适配器、跨进程回源合并、缓存熔断/失败退避、批量 CAS、自定义 codec、源数据版本校验。
这些扩展不能通过引入有副作用的 Load 或向业务暴露缓存消费操作实现。
