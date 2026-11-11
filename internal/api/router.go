package api

import (
	"asset-platform/internal/middleware"
	"asset-platform/internal/version"
	"net/http"

	"github.com/gin-gonic/gin"
)

// RegisterRoutes 注册所有路由
func RegisterRoutes(r *gin.Engine) {
	// IP 白名单中间件（配置 ALLOWED_IPS 环境变量，留空则不限制）
	r.Use(IPWhitelist())

	// 公开接口
	r.POST("/api/auth/login", Login)

	// 节点接口（agent 自动注册/心跳/拉任务/上报，公开）
	r.POST("/api/nodes/register", RegisterNode)
	r.POST("/api/nodes/:id/heartbeat", AgentAuth(), Heartbeat)
	r.GET("/api/nodes/:id/tasks", AgentAuth(), GetNodeTasks)
	r.POST("/api/nodes/:id/results", AgentAuth(), ReportNodeResults)

	// 免扫描清单（公开读，agent 拉取用于扫描排除）
	r.GET("/api/excludes", ListExcludes)

	// 任务日志 SSE 实时流（自己校验 query token，供原生终端使用）
	r.GET("/api/tasks/:id/log/stream", TaskLogStream)

	// 节点一键加入（令牌校验）
	r.GET("/api/nodes/install.sh", InstallSh)
	r.GET("/api/nodes/install.ps1", InstallPs1)
	r.GET("/api/nodes/agent-binary", AgentBinary)

	// agent 长连接（SSE 唤醒）与子任务上报（agent 调用，不依赖 JWT）
	r.GET("/api/agent/stream/:id", AgentAuth(), AgentStream)
	r.GET("/api/agent/nodes/:id/tasks", AgentAuth(), AgentTasks)
	r.GET("/api/agent/nodes/:id/license", AgentAuth(), GetNodeLicense)
	r.GET("/api/agent/tasks/:id", AgentTaskAuth(), AgentTaskDetail)
	r.POST("/api/agent/tasks/:id/progress", AgentTaskAuth(), AgentProgress)
	r.POST("/api/agent/tasks/:id/finish", AgentTaskAuth(), AgentFinish)

	// 需登录的接口
	// 健康检查（updater 边车调用，无需认证）
	r.GET("/api/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "version": version.Version})
	})

	auth := r.Group("/api")
	auth.Use(middleware.Auth())
	{
		auth.GET("/auth/me", Me)
		auth.POST("/auth/password", ChangePassword)
		auth.GET("/system/stats", SystemStats)
		auth.GET("/faults", ListFaults)
		auth.GET("/faults/retention", GetFaultsRetention)
		auth.POST("/faults/retention", SaveFaultsRetention)
		auth.GET("/system/fuse", GetFuseConfig)
		auth.POST("/system/fuse", SaveFuseConfig)
		auth.GET("/system/logs", SystemLogs)
		auth.GET("/system/version", SystemVersion)
		auth.POST("/fingerprint/identify", IdentifyFingerprint)
		// LLM
		auth.GET("/llm/status", LlmStatus)
		// LLM 配置管理
		auth.GET("/llm/profiles", ListLlmProfiles)
		auth.POST("/llm/profiles", CreateLlmProfile)
		auth.PUT("/llm/profiles/:id", UpdateLlmProfile)
		auth.DELETE("/llm/profiles/:id", DeleteLlmProfile)
		auth.POST("/llm/profiles/:id/test", TestLlmProfile)
		auth.POST("/llm/profiles/:id/reset", ResetLlmBreaker)
		auth.POST("/llm/breakers/reset-all", ResetAllLlmBreakers)
		auth.GET("/llm/usage", LlmUsageStats)
		// 会话
		auth.GET("/llm/conversations", ListConversations)
		auth.POST("/llm/conversations", CreateConversation)
		auth.GET("/llm/conversations/:id", GetConversation)
		auth.PUT("/llm/conversations/:id", RenameConversation)
		auth.DELETE("/llm/conversations/:id", DeleteConversation)
		auth.POST("/llm/conversations/clear-all", ClearAllConversations)
		auth.POST("/llm/chat-v2", LlmChatV2)
		auth.POST("/llm/chat", LlmChat)
		auth.GET("/llm/tools", LlmTools)
		auth.POST("/llm/tool-call", LlmToolCall)
		auth.GET("/llm/doc", LlmDoc)
		auth.POST("/fingerprint/batch", BatchIdentifyFingerprint)

		// 资产
		auth.GET("/assets", ListAssets)
		auth.GET("/assets/template", DownloadLedgerTemplate)
		auth.POST("/assets/import", ImportLedger)
		auth.GET("/assets/:ip/snapshots", IPHistory)
		auth.GET("/assets/:ip/ports", AssetPorts)
		auth.PUT("/assets/:id/owner", UpdateAssetOwner)

		// 通用导出（自选列）
		auth.POST("/export/excel", ExportExcel)
		auth.PUT("/assets/:id/confirm", ConfirmAsset)
		auth.POST("/assets/batch-delete", BatchDeleteAssets)
		auth.POST("/assets/batch-update", BatchUpdateAssets)
		auth.GET("/assets/import-conflicts", ListImportConflicts)
		auth.POST("/assets/import-conflicts/:id/resolve", ResolveImportConflict)
		auth.POST("/assets/import-conflicts/resolve-all", ResolveAllImportConflicts)

		// 资产处置（待确认 web 资产）
		auth.GET("/disposal", ListDisposal)
		auth.GET("/disposal/export", ExportDisposal)
		auth.DELETE("/disposal/:ip", DeleteDisposal)
		auth.POST("/disposal/batch-delete", BatchDeleteDisposal)
		auth.POST("/disposal/confirm-ledger", ConfirmByLedger)
		auth.POST("/disposal/confirm", ConfirmDisposal)

		// 组织结构（部门）
		auth.GET("/departments", ListDepartments)
		auth.POST("/departments/sync", SyncDepartmentsFromAssets)
		auth.POST("/departments/merge", MergeDepartments)
		auth.POST("/departments/split", SplitDepartment)
		auth.POST("/departments", CreateDepartment)
		auth.PUT("/departments/:id", UpdateDepartment)
		auth.DELETE("/departments/:id", DeleteDepartment)

		// 服务
		auth.GET("/services", ListServices)
		auth.DELETE("/services/:id", DeleteService)
		auth.POST("/services/batch-delete", BatchDeleteServices)

		// 任务
		auth.POST("/tasks", CreateTask)
		auth.PUT("/tasks/:id", UpdateTask)
		auth.GET("/tasks", ListTasks)
		auth.GET("/tasks/scheduled", ListScheduledTasks)
		auth.GET("/tasks/:id/overview", TaskOverview)
		auth.GET("/tasks/:id/runs", ListTaskRuns)
		auth.POST("/tasks/:id/run", RunTask)
		auth.POST("/tasks/:id/pause", PauseTask)
		auth.POST("/tasks/:id/resume", ResumeTask)
		auth.POST("/tasks/:id/restart", RestartTask)
		auth.POST("/tasks/:id/stop", StopTask)
		auth.POST("/tasks/:id/control", TaskControl)
		auth.DELETE("/tasks/:id", DeleteTask)
		auth.POST("/tasks/batch-delete", BatchDeleteTasks)
		auth.GET("/tasks/:id/log", TaskLog)

		// 扫描模板
		auth.GET("/templates", ListTemplates)
		auth.POST("/templates", UpsertTemplate)
		auth.DELETE("/templates/:id", DeleteTemplate)

		// 仪表盘
		auth.GET("/dashboard/summary", Dashboard)
		auth.GET("/dashboard/trend", ServiceTrend)

		// 合规
		auth.GET("/compliance/rules", ListRules)
		auth.POST("/compliance/rules", UpsertRule)
		auth.DELETE("/compliance/rules/:id", DeleteRule)
		auth.GET("/compliance/results", ListComplianceResults)
		auth.POST("/compliance/check", RunComplianceCheck)
		auth.DELETE("/compliance/results/:id", DeleteComplianceResult)
		auth.PUT("/compliance/results/:id", UpdateComplianceResult)
		auth.POST("/compliance/results/batch-delete", BatchDeleteComplianceResults)
		// AI 识别（手动触发单个服务，用于验证识别链路）
		auth.POST("/services/ai-identify", AIIdentifyService)
		auth.POST("/services/reidentify", ReidentifyServices)
		// 扫描监控 / AI 开关 / 备份恢复
		auth.POST("/agent-tasks/:id/ai-diagnose", AIDiagnoseShard)
		auth.GET("/agent-tasks/:id/diagnose-log", ShardDiagnoseLog)
		auth.GET("/agent-tasks/:id/log", ShardLog)
		auth.GET("/ai/settings", GetAISettings)
		auth.PUT("/ai/settings", UpdateAISettings)
		auth.GET("/backup/settings", GetBackupSettings)
		auth.PUT("/backup/settings", UpdateBackupSettings)
		auth.GET("/backups", ListBackups)
		auth.POST("/backups/run", RunBackupNow)
		auth.DELETE("/backups/:name", DeleteBackup)
		auth.POST("/backups/:name/restore", RestoreBackup)

		// 节点
		auth.GET("/nodes", ListNodes)
		auth.DELETE("/nodes/:id", DeleteNode)
		auth.POST("/nodes/join-token", CreateJoinToken)
		auth.PUT("/nodes/:id/config", UpdateNodeConfig)
		auth.PUT("/nodes/:id/license", UploadNodeLicense)
		auth.GET("/nodes/license/status", GetNodeLicenseStatus)
		auth.POST("/nodes/:id/upgrade", UpgradeNode)
		auth.GET("/nodes/agent-version", AgentVersionInfo)
		auth.POST("/nodes/:id/health", NodeHealthCheck)
		auth.GET("/nodes/:id/terminal", NodeTerminal)

		// 分布式子任务
		auth.GET("/tasks/:id/agent-tasks", TaskAgentTasks)
		auth.POST("/tasks/:id/dispatch", DispatchTask)
		auth.POST("/agent-tasks/:id/cancel", CancelAgentTask)
		auth.POST("/agent-tasks/:id/stop", StopAgentTask)

		// 页面快照
		auth.GET("/snapshots", ListPageSnapshots)
		auth.POST("/snapshots/capture", CaptureSnapshots)
		auth.GET("/snapshots/progress", GetSnapshotProgress)
		auth.GET("/snapshots/settings", GetSnapshotSettings)
		auth.POST("/snapshots/settings", SaveSnapshotSettings)
		auth.GET("/notify/settings", GetNotifySettings)
		auth.POST("/notify/settings", SaveNotifySettings)
		auth.POST("/notify/test", TestNotify)
		auth.GET("/system/settings", GetSystemSettings)
		auth.POST("/system/settings", SaveSystemSettings)
		auth.GET("/system/update/check", CheckUpdate)
		auth.POST("/system/update/apply", ApplyUpdate)
		auth.POST("/system/update/rollback", RollbackUpdate)
		auth.GET("/nodes/:id/projects", ListNodeProjects)
		auth.POST("/nodes/:id/projects/delete", DeleteNodeProjects)
		auth.GET("/snapshots/:id/image", SnapshotImage)
		auth.POST("/snapshots/batch-delete", DeleteSnapshots)

		// 网段（机房）
		auth.POST("/targets/preview", PreviewTargets)
		auth.GET("/segments", ListSegments)
		auth.POST("/segments", CreateSegment)
		auth.POST("/segments/batch", BatchCreateSegments)
		auth.POST("/excludes/batch", BatchCreateExcludes)
		auth.PUT("/segments/:id", UpdateSegment)
		auth.DELETE("/segments/:id", DeleteSegment)

		// 免扫描清单（写操作）
		auth.POST("/excludes", CreateExclude)
		auth.PUT("/excludes/:id", UpdateExclude)
		auth.DELETE("/excludes/:id", DeleteExclude)
	}
}
