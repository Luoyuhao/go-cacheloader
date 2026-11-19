# 设计与接入

- [string 访问契约](string.md)：只读值、Found、TTL、回源、刷新、CAS 及生命周期。
- [zset 快照契约](zset.md)：完整有序集合读取、排序/范围/空集合及规模边界。
- [接入设计与迁移](api-design.md)：当前 API、兼容性变化和未实现扩展。
- [决策记录](decisions.md)：当前实现边界。
- [sched 需求评估](sched-read-through.md)：候选读取与领取分层参考，不属于本库实现范围。

实现变动同步契约；代码位置只在 [代码地图](../.agents/code-map/README.md) 维护。
