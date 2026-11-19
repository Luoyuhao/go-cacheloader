# 代码地图

路径相对仓库根目录。新增/移动调用链时同步命中行；契约在 docs，不在地图重复实现论证。

| 任务 | 入口 | 回归 |
| --- | --- | --- |
| 快照后端、成员类型 | cacher.go：StringCacher、BatchStringCacher、Z | memory/cache_test.go、各 loader 测试 |
| 泛型回源与批量结果 | loader.go：LoadFunc、BatchLoadFunc、Result | string_cacheloader/cacheloader_test.go |
| 配置/默认值/错误 | config.go、errors.go | TestCloseAndValidation |
| string 业务入口 | string_cacheloader/cacheloader.go：New/Get/MGet/Close | string_cacheloader/cacheloader_test.go |
| 共享快照、CAS、超时、同 key 合并、准入、关闭 | internal/base_cacheloader.go：Reader | string_cacheloader/cacheloader_test.go |
| MGet 分批、可选批量 IO、结果校验与共享在途状态 | internal/batch.go、string_cacheloader/cacheloader.go：WithBatchLoader | string_cacheloader/batch_test.go |
| zset 完整快照、规范化排序、范围、基数 | zset_cacheloader/cacheloader.go：New/Get/ZRange/ZCard/Close | zset_cacheloader/cacheloader_test.go |
| 内存 LRU/TTL/CAS 后端 | memory/cache.go | memory/cache_test.go |
| JSON 辅助工具 | helper/json.go | helper/json_test.go |
| 接入与迁移 | docs/api-design.md | README.md、两种 loader 契约 |

zset 共用 string 的快照引擎，底层保存单个原子 string，不操作原生 Redis zset。
没有 Builder、Locker、SetCache、ZAdd/ZPop 或全局限流设置。
当前没有生产 Redis 适配器、批量 CAS 或跨进程回源合并；不要从测试内存后端推断这些能力。
