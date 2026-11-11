//go:build mipsle

package scanner

// mipsle 不注册 sqlite 驱动：modernc.org/sqlite（libc 预生成代码）不支持 MIPS。
// 影响仅限**本地审计落库**这一旁路功能：
//   - newAuditWriter 的 sql.Open("sqlite") 报 unknown driver → 记一行日志后返回 nil，
//     扫描/识别/流式上报全部照常（旁路语义，主链路零依赖）；
//   - ListProjects 心跳带空列表、cleanup: 指令记失败日志，不影响任务执行。
// 老 MIPS 网关/嵌入式设备本就只当哑节点跑扫描，可接受。
