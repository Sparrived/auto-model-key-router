// Package quota 是「账号额度」的归一读法与额度冻结表的宿主。
//
// 它有两个互不依赖的部分：
//
//   - window.go：把上游的额度窗口（Anthropic 的 unified 限额头、Codex 的
//     x-codex-* 头，以及 CPA 被动采集信号）解析成同一套 Window；
//   - freeze.go：额度冻结表的持久化容器，供选路与「账号资源 / 供应商」两个页面共用。
//
// 本包**不**知道配置、HTTP 客户端与选路：判定「该不该冻」是 api.QuotaGuard 的事，
// 本包只回答「这个窗口算不算硬性耗尽」与「这把 Key 现在冻没冻」。
package quota
