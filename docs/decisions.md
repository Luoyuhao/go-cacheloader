# 决策记录

| 状态 | 决策 |
| --- | --- |
| 已实现 | string 与 zset 都是只读源数据快照；对业务不开放缓存写/消费 |
| 已实现 | typed LoadFunc + Config；T any；Found 区分不存在与合法零值 |
| 已实现 | zset 使用完整有序快照，复用 string 原子存储；独立于 sip 任务队列需求 |
| 已实现 | 成功回填/刷新重置 TTL；TTL=0 表示不过期且仍支持刷新 |
| 已实现 | envelope 随机 revision + 原子 CAS，拒绝迟到覆盖及过期复活 |
| 已实现 | 同 key 回源合并，每实例任务准入、分阶段超时、Close 和非致命错误观察 |
| 已实现 | memory 后端提供有容量上限的 LRU、TTL 和 CAS |
| 已实现 | MGet 去重、分批、保序及独立值；可选缓存批量接口与 WithBatchLoader，未提供则单 key 回退 |
| 未实现 | 批量 CAS、Redis 适配器、熔断/退避、跨实例请求合并、DB 版本校验 |

[sched 需求评估](sched-read-through.md) 保留为候选读取与领取边界的参考，不能据此给快照库增加 Claim/Ack。
实现契约见 [string](string.md) 与 [zset](zset.md)；迁移成本见 [接入设计](api-design.md)。
