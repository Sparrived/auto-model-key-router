package metrics

// 本文件是字节级兼容的核心：SQLite 会把 CREATE TABLE / CREATE INDEX 的语句
// **原文**存进 sqlite_master.sql，任何缩进或换行差异都会让“同一个库”看起来
// 结构不同（也会让后续 dump/比对工具报差异）。所以下面的字面量是从
// auto_model_key_router/metrics.py 的 _init_schema 逐字符抄录的，不要 gofmt
// 之外的重排、不要改成 raw string、不要顺手对齐。
//
// 抄录时的三引号内容以 12 个空格为基准缩进（Python 源码所在层级），与
// metrics.py:886-909 的第 16 空格列定义一致。

// createTableSQL 对应 metrics.py:886-909。
const createTableSQL = `
            CREATE TABLE IF NOT EXISTS request_metrics (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                created_at TEXT NOT NULL,
                caller_type TEXT NOT NULL DEFAULT 'local',
                model_id TEXT NOT NULL,
                requested_model_id TEXT NOT NULL,
                provider_id TEXT,
                pool_name TEXT,
                upstream_model_id TEXT,
                key_name TEXT NOT NULL,
                status_code INTEGER,
                success INTEGER NOT NULL,
                retried INTEGER NOT NULL,
                prompt_tokens INTEGER NOT NULL DEFAULT 0,
                completion_tokens INTEGER NOT NULL DEFAULT 0,
                total_tokens INTEGER NOT NULL DEFAULT 0,
                cached_tokens INTEGER NOT NULL DEFAULT 0,
                cache_creation_input_tokens INTEGER NOT NULL DEFAULT 0,
                cache_read_input_tokens INTEGER NOT NULL DEFAULT 0,
                first_token_ms INTEGER NOT NULL DEFAULT 0,
                duration_ms INTEGER NOT NULL DEFAULT 0
            )
            `

// backfillStatements 对应 metrics.py:910-918 的两条 UPDATE。
//
// caller_type 的历史值可能是 NULL 或早期写错的其它字符串，统一收敛到 'local'；
// requested_model_id 是后加的列，老行默认空串，用 model_id 回填。
//
// 合法值表在这里必须与 server/query.go 的 callerTypes 一致：这条 UPDATE 只在
// caller_type 列**不存在**时才执行（ensureColumn 里 `if exists { return nil }`），
// 因此它不会碰到新写入的 'workspace' / 'access_key' 行；但漏掉一档会让一次老库升级
// 把那种行静默改写成 'local'，指标永久失真。
//
// visitor 档已随访客模式删除：老库里残留的 'visitor' 行会在下一次缺列升级时被收敛到
// 'local'。这是可接受的——那些流量来自一把已不存在的凭据，没有更强的归属可还原。
var backfillStatements = [...]string{
	"UPDATE request_metrics SET caller_type = 'local' WHERE caller_type IS NULL OR caller_type NOT IN ('local', 'workspace', 'access_key')",
	"UPDATE request_metrics SET requested_model_id = model_id WHERE requested_model_id = ''",
}

// columnUpgrades 对应 metrics.py:912-927 的 _ensure_column 调用序列。
//
// 顺序有意义：老库缺列时按此顺序 ALTER，保证列序与全新建库一致（sqlite_master
// 的列序影响 SELECT * 与 table_info 的输出顺序）。名称/定义分列两段，因为它
// 同时用于 PRAGMA table_info 的存在性判断与 ALTER TABLE 的语句拼接。
var columnUpgrades = [...]struct{ Name, Definition string }{
	{"caller_type", "TEXT NOT NULL DEFAULT 'local'"},
	{"requested_model_id", "TEXT NOT NULL DEFAULT ''"},
	{"provider_id", "TEXT"},
	{"pool_name", "TEXT"},
	{"upstream_model_id", "TEXT"},
	{"cached_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"cache_creation_input_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"cache_read_input_tokens", "INTEGER NOT NULL DEFAULT 0"},
	{"first_token_ms", "INTEGER NOT NULL DEFAULT 0"},
	{"duration_ms", "INTEGER NOT NULL DEFAULT 0"},
}

// createWorkspaceTableSQL 是工作空间旁挂表的建表语句。
//
// **本包唯一有意新增的库对象**（参照实现没有工作空间），因此不对应 metrics.py 的
// 任何一行。放成旁挂表而不是给 request_metrics 加一列，理由是兼容成本：
//
//   - request_metrics 的建表原文、列序与索引定义是**对外兼容性契约**：旧二进制要
//     能继续读写同一个库，靠的就是这份定义不被改写。而 ALTER TABLE ADD COLUMN
//     **会重写 sqlite_master.sql**（实测：即便在全新库上也会把新列以
//     ", workspace TEXT NOT NULL DEFAULT ”" 的形式追加到原文末尾），加列必然
//     改写这份定义，代价由所有既有二进制承担。
//   - 旁挂表让 request_metrics 自身保持逐字节不变，只在 sqlite_master 里多出一条
//     **新表**的条目，差异面从「表定义被改写」缩小到「多了一张表」。
//   - 旧二进制打开新库时只是看不到这张表，仍能正常读写指标；加列则会遇到它不认识
//     的列序（SELECT * 与 table_info 的输出都会变）。
//
// request_id 就是 request_metrics.id（同库自增主键），INTEGER PRIMARY KEY 让它
// 成为 rowid 别名：一对一约束与查询索引同时到手，且**不会**在 sqlite_master 里
// 多出一条索引条目（实测条目数只 +1）。
//
// workspace 恒为非空：写入方（internal/proxy 与装配层）已经过
// config.NormalizeWorkspace 归一化。**没有归属的行不写这张表**（历史行、以及不走
// proxy 的 runtime 接缝），查询端把它们算进 unattributed——不能在这里兜底成
// default，那会把升级前的旧账算到默认工作空间头上。
const createWorkspaceTableSQL = `
            CREATE TABLE IF NOT EXISTS request_workspace (
                request_id INTEGER PRIMARY KEY,
                workspace TEXT NOT NULL
            )
            `

// insertWorkspaceSQL 写入一行工作空间归属，对应每次 record() 的第二次写入。
const insertWorkspaceSQL = `
        INSERT OR REPLACE INTO request_workspace (request_id, workspace) VALUES (?, ?)
        `

// createAccessKeyTableSQL 是访问密钥旁挂表的建表语句。
//
// 与 request_workspace 完全同构，理由也相同：不改 request_metrics 的列序，旧二进制
// 打开新库只是看不到这张表，仍能正常读写指标。
//
// **为什么需要它**：request_metrics.caller_type 只能说明「这把流量来自访问密钥」，
// 说不出**来自哪一把**——而访问密钥是按人分发的，两把 key 的流量在数据层混在一起
// 就回答不了「这个人用了多少」，而那正是访问密钥看板的全部意义。注意
// request_metrics.key_name 是**被选中的上游 Provider key 名**（internal/proxy 的
// recordMetric 写的是 key.Name，key 是 config.KeyConfig），与调用方身份无关，
// 不能拿来替代这张表。
//
// 工作空间旁挂表也替代不了：访问密钥不绑定工作空间，它的请求照常带
// X-AMKR-Workspace（归一化后通常是 default），因此两把访问密钥会落进同一个
// workspace 值。
//
// 存的是配置里的 key_id（稳定标识符）而不是明文 key 或显示名：显示名可改，明文不该
// 进指标库。没有归属的行（完整权限、工作空间推理凭据、历史行）不写这张表。
const createAccessKeyTableSQL = `
            CREATE TABLE IF NOT EXISTS request_access_key (
                request_id INTEGER PRIMARY KEY,
                access_key_id TEXT NOT NULL
            )
            `

// insertAccessKeySQL 写入一行访问密钥归属，与工作空间归属同一次 record() 的写入。
const insertAccessKeySQL = `
        INSERT OR REPLACE INTO request_access_key (request_id, access_key_id) VALUES (?, ?)
        `

// createAccessKeyIndexSQL 给访问密钥旁挂表的查找列建索引。
//
// 与 request_workspace 的差别：那张表按 request_id 一对一 JOIN，工作空间的过滤发生在
// **JOIN 之后**（`w.workspace = ?`），SQLite 仍会先走 rowid。而访问密钥读数是
// 「先按 access_key_id 选出这把 key 的行」（accesskey.go 的 accessKeyWindow），
// 没有索引就是整张旁挂表全扫——看板每次刷新都付这个代价。
//
// 建在新表上是安全的：request_metrics 的 7 条索引一字未动，这里是本包自己的对象。
const createAccessKeyIndexSQL = `
        CREATE INDEX IF NOT EXISTS idx_request_access_key ON request_access_key(access_key_id)
        `

// createRequestSourceTableSQL 是请求来源旁挂表的建表语句。
//
// 与 request_workspace / request_access_key 同构，理由相同（见
// createWorkspaceTableSQL）：只在 sqlite_master 里多一条新表条目，request_metrics
// 自身的建表原文与列序逐字节不变，旧二进制打开新库照常读写指标。
//
// **为什么需要它**：WebUI 的请求流要回答「这次请求是谁、从哪儿发来的」。
// request_metrics.caller_type 只说得出凭据档位（本机 / 工作空间 / 访问密钥），
// request_workspace 只说得出空间归属，两者都答不出**发起方的网络位置**。
//
// client_addr 存入站请求的 RemoteAddr（host:port，IPv6 形如 `[::1]:50874`），与
// 访问日志（internal/server/accesslog.go）取的是同一个值。**刻意不读
// X-Forwarded-For**：那个头由调用方自带、可伪造，把它当来源会让看板显示一个
// 攻击者选定的 IP，比没有来源更糟。
//
// user_agent 允许为空（curl 之类可以不带头）。没有 client_addr 的行不写这张表：
// 历史行与不走 HTTP 的写入路径不会凭空获得一个来源，查询端把它们渲染成 null。
const createRequestSourceTableSQL = `
            CREATE TABLE IF NOT EXISTS request_source (
                request_id INTEGER PRIMARY KEY,
                client_addr TEXT NOT NULL,
                user_agent TEXT
            )
            `

// insertRequestSourceSQL 写入一行请求来源，与另外两张旁挂表同一次 record() 的写入。
const insertRequestSourceSQL = `
        INSERT OR REPLACE INTO request_source (request_id, client_addr, user_agent) VALUES (?, ?, ?)
        `

// createRequestShapeTableSQL 是请求形态旁挂表的建表语句。
//
// 与 request_workspace / request_access_key / request_source 同构，理由相同（见
// createWorkspaceTableSQL）：只在 sqlite_master 里多一条新表条目，request_metrics
// 自身的建表原文与列序逐字节不变，旧二进制打开新库照常读写指标。
//
// **为什么需要它**：request_metrics 记的是**结果**（状态码、耗时、token），记不下
// 「这次请求长什么样」。少了形态，事后分不出同一条 200 是流式还是非流式、走的是哪条
// API 路径、以什么推理强度发出去的——而这三件事恰好决定了排查时该去看哪一层。
//
// api_format 存**入站路径**（chat/completions、messages、responses、embeddings、
// images/*）而不折算成方言名：同一路径既可能原生透传、也可能被改写成 chat 形态，归一
// 必然丢掉这层区别，而路径本身是稳定的分类（未知路径照原样记下，不静默归并）。
//
// reasoning_effort 允许为空：模型配置与请求体都没给强度时它就是 NULL，表示「没有生效
// 的强度」，而不是某个默认档。没有 api_format 的行不写这张表：历史行与不走 proxy 的
// 写入路径不会凭空获得一个形态，查询端把它们渲染成 null。
const createRequestShapeTableSQL = `
            CREATE TABLE IF NOT EXISTS request_shape (
                request_id INTEGER PRIMARY KEY,
                stream INTEGER NOT NULL,
                api_format TEXT NOT NULL,
                reasoning_effort TEXT
            )
            `

// insertRequestShapeSQL 写入一行请求形态，与另外三张旁挂表同一次 record() 的写入。
const insertRequestShapeSQL = `
        INSERT OR REPLACE INTO request_shape (request_id, stream, api_format, reasoning_effort) VALUES (?, ?, ?, ?)
        `

// createIndexStatements 对应 metrics.py:929-947 的 7 条索引。
//
// requested_model_id / caller / provider / upstream_model 四类维度是后加的，
// 对应的查询都要按 created_at 做窗口过滤，所以索引是多列复合而非单列。
var createIndexStatements = [...]string{
	`CREATE INDEX IF NOT EXISTS idx_request_metrics_model ON request_metrics(model_id)`,
	`CREATE INDEX IF NOT EXISTS idx_request_metrics_requested_model ON request_metrics(requested_model_id)`,
	`CREATE INDEX IF NOT EXISTS idx_request_metrics_key ON request_metrics(model_id, key_name)`,
	`CREATE INDEX IF NOT EXISTS idx_request_metrics_created ON request_metrics(created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_request_metrics_caller ON request_metrics(caller_type, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_request_metrics_provider ON request_metrics(provider_id, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_request_metrics_upstream_model ON request_metrics(upstream_model_id, created_at)`,
}

// usageAggregates 对应 metrics.py:22-39 的 _USAGE_AGGREGATES。
//
// 17 个聚合、16 个 SUM/MIN/MAX 表达式。注意：
//   - failures 是 SUM(CASE WHEN success = 0 ...) 而非 COUNT(*) - SUM(success)，
//     两者在 success 为 NULL 时结果不同，这里必须照抄；
//   - MIN(duration_ms) / MIN(first_token_ms) 没有 COALESCE，空集时是 NULL，
//     由 _stats_from_aggregate 映射成 Python 的 None，再在 to_dict 里 `or 0`
//     渲染为 0；
//   - 这里**没有分位数聚合**（P95 等）。参照实现从未有过，别加。
const usageAggregates = `
    COUNT(*) AS requests,
    COALESCE(SUM(success), 0) AS successes,
    COALESCE(SUM(CASE WHEN success = 0 THEN 1 ELSE 0 END), 0) AS failures,
    COALESCE(SUM(retried), 0) AS retries,
    COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
    COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
    COALESCE(SUM(total_tokens), 0) AS total_tokens,
    COALESCE(SUM(cached_tokens), 0) AS cached_tokens,
    COALESCE(SUM(cache_creation_input_tokens), 0) AS cache_creation_input_tokens,
    COALESCE(SUM(cache_read_input_tokens), 0) AS cache_read_input_tokens,
    COALESCE(SUM(duration_ms), 0) AS total_duration_ms,
    MIN(duration_ms) AS min_duration_ms,
    COALESCE(MAX(duration_ms), 0) AS max_duration_ms,
    COALESCE(SUM(first_token_ms), 0) AS total_first_token_ms,
    MIN(first_token_ms) AS min_first_token_ms,
    COALESCE(MAX(first_token_ms), 0) AS max_first_token_ms
`

// insertSQL 对应 metrics.py:196-233 的 INSERT。
//
// 列序与 created_at 位置都是兼容面的一部分：老库已存在，INSERT 必须按名写入
// （SQLite 允许省略有默认值/可空的列），这里显式列全部 19 个业务列。
const insertSQL = `
        INSERT INTO request_metrics (
            created_at,
            caller_type,
            model_id,
            requested_model_id,
            provider_id,
            pool_name,
            upstream_model_id,
            key_name,
            status_code,
            success,
            retried,
            prompt_tokens,
            completion_tokens,
            total_tokens,
            cached_tokens,
            cache_creation_input_tokens,
            cache_read_input_tokens,
            first_token_ms,
            duration_ms
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        `
