package metrics

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Sparrived/auto-model-key-router/internal/canonical"
	// 纯 Go 的 SQLite 实现：不依赖 CGO，静态二进制在精简镜像里也能跑。
	// 参照实现用 Python 标准库的 sqlite3（同样是嵌入的 C 库），这里换成
	// modernc 版本；两者操作同一份文件格式，实测 schema、strftime 与整数除法
	// 语义一致。
	_ "modernc.org/sqlite"
)

// pragmaDSN 是连接参数里的 PRAGMA 设置，对应 _configure_connection。
//
// modernc.org/sqlite 在每个新连接上执行 _pragma 里的语句，因此连接池扩容后的
// 新连接也带着同样的设置（实测 4 条连接全部生效）。synchronous 与 busy_timeout
// 是**连接级**的，必须走这里；journal_mode=WAL 是**持久化**在库文件上的，但
// 一并写上可以让只读打开旧库时也能切到 WAL。
const pragmaDSN = "_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"

// readerConns 是读连接池上限。
//
// ponytail: 参照实现只有**一条** `sqlite3.connect(check_same_thread=False)` 连接，
// 所有操作（含查询）都排在 asyncio.Lock + threading.Lock 后面串行执行。Go 侧改成
// 「一条写连接 + 一个读连接池」，这是**有意的偏离**：management API 的 /metrics
// 在 16 万行库上要跑好几秒，串行会让所有写入一起饿死。WAL 允许多读单写，语义上
// 与参照实现等价（每条记录仍是一次原子 INSERT，查询看到的是某个提交点的一致快照），
// 且不需要读锁。升级路径：若日后发现读到的快照与 Python 不完全可比，把
// SetMaxOpenConns 调成 1 即可退回串行语义。
const readerConns = 4

// nowBeijing 可被测试替换，对应测试里 monkeypatch metrics_module._now_beijing。
var nowBeijing = func() time.Time { return time.Now().In(beijingTZ) }

// SetNowForTest 替换包内时钟并返回还原函数；仅供测试使用。
//
// 这是给**别的包**留的测试缝隙：包内测试直接改 nowBeijing 就够了，但跨包测试
// 改不了未导出的变量，需要能先把时钟钉在一个固定时刻再断言（历史上对应参照
// 实现测试对 metrics_module._now_beijing 的 monkeypatch）。不改动任何生产路径。
func SetNowForTest(now func() time.Time) (restore func()) {
	previous := nowBeijing
	nowBeijing = now
	return func() { nowBeijing = previous }
}

// Store 对应 metrics.py 的 MetricsStore。
//
// 写操作由 writeMu 串行化：SQLite 同一时刻只允许一个写事务，串行化把
// SQLITE_BUSY 重试变成不可能发生，也让「写入顺序 == id 顺序」这一隐含假设成立
// （request_history 的 before_id 分页依赖它）。
type Store struct {
	path    string
	readDB  *sql.DB
	writeDB *sql.DB

	writeMu sync.Mutex
	stateMu sync.Mutex

	startedAt time.Time
	active    int
	closed    bool
	onRecord  func()
}

// RecordParams 是 record() 的参数（metrics.py:143）。
//
// 用指针表达 Python 的可选参数（status_code / provider_id / pool_name /
// upstream_model_id），因为空字符串是有效取值，不能与 None 混同。
//
// Workspace 是**有意增补**的字段（参照实现没有工作空间）：它不落在
// request_metrics 里，而是写进 request_workspace 旁挂表。空串表示「没有归属」——
// 写入方不写旁挂表，该行因此不会被任何工作空间统计到（升级前的历史行同样如此）。
type RecordParams struct {
	ModelID          string
	KeyName          string
	StatusCode       *int64
	Usage            *canonical.Value
	Retried          bool
	Failed           bool
	DurationMS       int64
	FirstTokenMS     int64
	RequestedModelID *string
	CallerType       string
	ProviderID       *string
	PoolName         *string
	UpstreamModelID  *string
	Workspace        string
	// AccessKeyID 是发起本次请求的访问密钥的 key_id（配置里的稳定标识符）。
	//
	// 空串表示这次请求不出自访问密钥（完整权限、工作空间推理凭据，以及所有历史写入
	// 路径）。它不落 request_metrics 的列，而是单独进 request_access_key 旁挂表，
	// 理由见 schema.go。
	//
	// 与 Workspace **互不排斥**：访问密钥照常带工作空间头，两者会各写各的旁挂表。
	AccessKeyID string
	// ClientAddr 是发起本次请求的客户端地址（入站请求的 RemoteAddr，host:port）。
	//
	// 空串表示这次写入没有来源可记（历史路径、不走 HTTP 的写入路径，以及测试里的
	// 最小参数）。它不落 request_metrics 的列，而是进 request_source 旁挂表，
	// 理由见 schema.go。
	ClientAddr string
	// UserAgent 是入站请求的 User-Agent，与 ClientAddr 同表；允许为空。
	UserAgent string
}

// Open 打开（必要时创建）指标库，对应 MetricsStore.__init__。
//
// 空目录会被创建，与 `parent.mkdir(parents=True, exist_ok=True)` 一致。
func Open(databasePath string) (*Store, error) {
	path := filepath.Clean(databasePath)
	// Python 的 `if self.database_path.parent != Path("")`：只有相对路径带目录
	// 部分（或绝对路径）时才建目录。
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}

	store := &Store{path: path, startedAt: nowBeijing()}
	readDB, err := sql.Open("sqlite", path+"?"+pragmaDSN)
	if err != nil {
		return nil, err
	}
	readDB.SetMaxOpenConns(readerConns)
	writeDB, err := sql.Open("sqlite", path+"?"+pragmaDSN)
	if err != nil {
		readDB.Close()
		return nil, err
	}
	writeDB.SetMaxOpenConns(1)
	store.readDB = readDB
	store.writeDB = writeDB

	if err := store.initSchema(); err != nil {
		store.Close()
		return nil, err
	}
	return store, nil
}

// Path 返回库文件路径，对应用于 snapshot 的 database_path。
func (s *Store) Path() string { return s.path }

// StartedAt 返回进程内该 store 的创建时间，对应 _started_at。
func (s *Store) StartedAt() time.Time { return s.startedAt }

// SetOnRecord 注册写后回调，对应 on_record。
//
// ponytail: 参照实现是 `Callable[[], Awaitable[None]]`，await 之后才从 record()
// 返回。Go 侧只保留同步回调：它的唯一用途是唤醒 SSE 广播循环（app.py:134），
// 而那是个"丢一次也无所谓"的唤醒信号。升级路径：真需要背压时改成带 context 的
// 异步接口，由调用方决定是否等待。
func (s *Store) SetOnRecord(callback func()) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.onRecord = callback
}

// AcquireActive 对应 acquire_active。
func (s *Store) AcquireActive() {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.active++
}

// ReleaseActive 对应 release_active：下限钳到 0，因为租约可能在异常路径上多释放。
func (s *Store) ReleaseActive() {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.active > 0 {
		s.active--
	}
}

// ActiveCount 对应 active_count。
func (s *Store) ActiveCount() int {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.active
}

// Close 对应 close()。
//
// 未移植 asyncio.shield：参照实现用它保证「await 被取消时工作线程仍跑完再抛」，
// 因为共享的 sqlite 连接不能被 close() 抢走。Go 侧没有可取消的协程切换到工作
// 线程这一层——写锁 + 连接池天然给出同样的安全性（Close 拿到 writeMu 时不会
// 有在途写）。这是刻意的省略，不是遗漏。
func (s *Store) Close() error {
	s.stateMu.Lock()
	if s.closed {
		s.stateMu.Unlock()
		return nil
	}
	s.closed = true
	s.stateMu.Unlock()

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var firstErr error
	if s.writeDB != nil {
		if err := s.writeDB.Close(); err != nil {
			firstErr = err
		}
	}
	if s.readDB != nil {
		if err := s.readDB.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// initSchema 对应 _init_schema（metrics.py:885）。
//
// 全部语句在**一个**写锁里执行，最后不显式 BEGIN/COMMIT：DDL 与 UPDATE 各自
// 自动提交，与 Python 的隐式事务 + 最后 commit() 效果相同（Python 的 sqlite3
// 在 DDL 前会隐式提交，本来也不是一个原子事务）。
func (s *Store) initSchema() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.writeDB.Exec(createTableSQL); err != nil {
		return err
	}
	// 列升级与两条回填交错执行，顺序照抄 metrics.py:912-919。
	for _, upgrade := range columnUpgrades {
		if err := s.ensureColumn(upgrade.Name, upgrade.Definition); err != nil {
			return err
		}
		switch upgrade.Name {
		case "caller_type":
			if _, err := s.writeDB.Exec(backfillStatements[0]); err != nil {
				return err
			}
		case "requested_model_id":
			if _, err := s.writeDB.Exec(backfillStatements[1]); err != nil {
				return err
			}
		}
	}
	for _, statement := range createIndexStatements {
		if _, err := s.writeDB.Exec(statement); err != nil {
			return err
		}
	}
	// 工作空间旁挂表（本包唯一有意新增的库对象，理由见 schema.go）。
	// 放在索引之后：它不触碰 request_metrics 的任何既有对象，顺序对旧库升级无影响，
	// 新建库与旧库升级因此得到同一份结构。
	if _, err := s.writeDB.Exec(createWorkspaceTableSQL); err != nil {
		return err
	}
	// 访问密钥旁挂表（理由见 schema.go）。与工作空间旁挂表同一位置：只在
	// sqlite_master 里多一条新表条目，request_metrics 自身逐字节不变。
	if _, err := s.writeDB.Exec(createAccessKeyTableSQL); err != nil {
		return err
	}
	// 它的查找索引（理由见 schema.go）：看板按 access_key_id 过滤，没索引就是全表扫。
	if _, err := s.writeDB.Exec(createAccessKeyIndexSQL); err != nil {
		return err
	}
	// 请求来源旁挂表（理由见 schema.go）。与另外两张旁挂表同一位置：只在
	// sqlite_master 里多一条新表条目，request_metrics 自身逐字节不变。
	//
	// 它按 request_id 一对一 JOIN，不需要自己的索引（与 request_workspace 同理）。
	if _, err := s.writeDB.Exec(createRequestSourceTableSQL); err != nil {
		return err
	}
	return nil
}

// ensureColumn 对应 _ensure_column（metrics.py:951）。
func (s *Store) ensureColumn(name, definition string) error {
	rows, err := s.writeDB.Query("PRAGMA table_info(request_metrics)")
	if err != nil {
		return err
	}
	exists := false
	for rows.Next() {
		var (
			cid        int64
			columnName string
			columnType string
			notNull    int64
			defaultVal sql.NullString
			pk         int64
		)
		if err := rows.Scan(&cid, &columnName, &columnType, &notNull, &defaultVal, &pk); err != nil {
			rows.Close()
			return err
		}
		if columnName == name {
			exists = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if exists {
		return nil
	}
	// 标识符来自本文件的常量表，不来自用户输入，因此不存在注入面。
	_, err = s.writeDB.Exec("ALTER TABLE request_metrics ADD COLUMN " + name + " " + definition)
	return err
}

// Record 对应 record()（metrics.py:143）。
func (s *Store) Record(params RecordParams) error {
	usage := normalizeUsage(params.Usage)
	requestModelID := params.ModelID
	if params.RequestedModelID != nil && *params.RequestedModelID != "" {
		requestModelID = *params.RequestedModelID
	}
	callerType := "local"
	// 白名单：不在列的取值会被静默改写成 local（那是权限最高的一档）。新增档位时
	// **必须**同步这里与 schema.go 的回填合法值表，漏一档会让看板把受限流量显示成
	// 主凭据流量。
	if params.CallerType == "local" || params.CallerType == "workspace" || params.CallerType == "access_key" {
		callerType = params.CallerType
	}
	failure := params.Failed || params.StatusCode == nil || *params.StatusCode >= 400

	s.writeMu.Lock()
	err := s.recordSync(params, usage, requestModelID, callerType, failure)
	s.writeMu.Unlock()
	if err != nil {
		return err
	}

	s.stateMu.Lock()
	callback := s.onRecord
	s.stateMu.Unlock()
	if callback != nil {
		callback()
	}
	return nil
}

// recordSync 对应 _record_sync（metrics.py:183）。
func (s *Store) recordSync(
	params RecordParams,
	usage Usage,
	requestModelID, callerType string,
	failure bool,
) error {
	success := int64(1)
	if failure {
		success = 0
	}
	retried := int64(0)
	if params.Retried {
		retried = 1
	}
	// Python 用 max(x, 0) 钳住负数：上游可能报负的耗时。
	firstTokenMS := params.FirstTokenMS
	if firstTokenMS < 0 {
		firstTokenMS = 0
	}
	durationMS := params.DurationMS
	if durationMS < 0 {
		durationMS = 0
	}
	result, err := s.writeDB.Exec(
		insertSQL,
		// 必须走 formatISO：直接 .Format("2006-01-02T15:04:05") 会**丢掉微秒**，
		// 而 Python 的 isoformat() 在有微秒时保留 6 位。丢微秒不只是精度问题：
		// 同一秒内的多条记录会变成同一个 created_at 字符串，使字典序窗口边界
		// （created_at <= now）把本该排除的记录算进来。
		formatISO(nowBeijing()),
		callerType,
		params.ModelID,
		requestModelID,
		nullableParam(params.ProviderID),
		nullableParam(params.PoolName),
		nullableParam(params.UpstreamModelID),
		params.KeyName,
		nullableParam(params.StatusCode),
		success,
		retried,
		usage.PromptTokens,
		usage.CompletionTokens,
		usage.TotalTokens,
		usage.CachedTokens,
		usage.CacheCreationInputTokens,
		usage.CacheReadInputTokens,
		firstTokenMS,
		durationMS,
	)
	if err != nil {
		return err
	}
	// 三张旁挂表都写在这里，**必须在 writeMu 内**（调用方已持锁）：LastInsertId
	// 是连接级状态，writeDB 恰好只有一条连接（SetMaxOpenConns(1)），因此这里读到
	// 的就是刚插入的那一行——换连接池就会读到别的连接的上一次插入。
	//
	// 用 INSERT OR REPLACE 而非 INSERT：request_id 是主键，重放同一行（测试里的
	// 重复写入）不应炸掉，且替换语义与"归属只有一份"一致。
	//
	// 各表独立判空：访问密钥的请求**同时**有工作空间归属（它照常带
	// X-AMKR-Workspace），因此不能用 `if params.Workspace == ""` 提前 return，
	// 那会把访问密钥那一行整个丢掉。
	if params.Workspace != "" || params.AccessKeyID != "" || params.ClientAddr != "" {
		requestID, err := result.LastInsertId()
		if err != nil {
			return err
		}
		// 没有归属就不写：历史行与不走 proxy 的写入路径都不会凭空获得一个工作空间。
		if params.Workspace != "" {
			if _, err := s.writeDB.Exec(insertWorkspaceSQL, requestID, params.Workspace); err != nil {
				return err
			}
		}
		// 同理：非访问密钥的请求不会凭空获得一个访问密钥归属。
		if params.AccessKeyID != "" {
			if _, err := s.writeDB.Exec(insertAccessKeySQL, requestID, params.AccessKeyID); err != nil {
				return err
			}
		}
		// 同理：没有来源地址的行不写来源旁挂表。user_agent 为空时写 NULL 而不是
		// 空串，让「没带这个头」在库里只有一种表示。
		if params.ClientAddr != "" {
			if _, err := s.writeDB.Exec(insertRequestSourceSQL,
				requestID, params.ClientAddr, nullableText(params.UserAgent)); err != nil {
				return err
			}
		}
	}
	return nil
}

// nullableText 把空串写成 SQL NULL，非空原样写入。
func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// nullableParam 把可选参数转成驱动可接受的 nil/值。
func nullableParam[T any](value *T) any {
	if value == nil {
		return nil
	}
	return *value
}

// Snapshot 对应 snapshot()（metrics.py:247）。
//
// since 优先于 hours：两者都给时忽略 hours 算出的窗口起点（但 window.hours 仍
// 原样回显）。
func (s *Store) Snapshot(hours *float64, since *time.Time) (*canonical.Value, error) {
	now := nowBeijing()
	return s.snapshotSync(hours, since, now)
}

// snapshotSync 对应 _snapshot_sync（metrics.py:680）。
func (s *Store) snapshotSync(hours *float64, since *time.Time, now time.Time) (*canonical.Value, error) {
	var sinceStr *string
	if since != nil {
		value := formatISO(*since)
		sinceStr = &value
	} else if hours != nil {
		value := formatISO(addHours(now, *hours))
		sinceStr = &value
	}

	untilStr := formatISO(now)
	// 窗口内的 9 个分组由一次最细粒度扫描上卷得到（见 rollup.go 的成本说明）：
	// 归属维度只统计三个归属列全非空、未归属统计其余的行，这两种互补口径都在
	// 那一层表达，这里不再逐个拼 WHERE。
	groups, err := s.snapshotRollup(sinceStr, &untilStr)
	if err != nil {
		return nil, err
	}
	totalStats := groups.total.get(dimKey{})

	rateStart := formatISO(addSeconds(now, RateWindowSeconds))
	// rate 窗口只有几分钟，行数极少；这里继续用参照实现的 queryStats，一分钱不省，
	// 但让那份实现留在链路上（它也是上卷结果的对照物）。
	recent, err := s.queryStats(nil, &rateStart, &untilStr, true)
	if err != nil {
		return nil, err
	}
	recentStats := recent.get(dimKey{})

	// 即使窗口内没有该档流量也要出现这些键，前端据此渲染固定分组。
	//
	// visitor 档已随访客模式删除，新增 access_key 档（访问密钥的流量）。保留 local 与
	// workspace：三档分别对应主凭据、工作空间推理凭据与访问密钥。
	groups.callerTypes.setDefault(dimKey{N: 1, A: "local"})
	groups.callerTypes.setDefault(dimKey{N: 1, A: "workspace"})
	groups.callerTypes.setDefault(dimKey{N: 1, A: "access_key"})

	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "count_semantics", Value: canonical.NewString(CountSemantics)},
		canonical.ObjectPair{Key: "window", Value: canonical.NewObjectOf(
			canonical.ObjectPair{Key: "from", Value: nullableStringValue(sinceStr)},
			canonical.ObjectPair{Key: "to", Value: canonical.NewString(untilStr)},
			canonical.ObjectPair{Key: "hours", Value: hoursValue(hours)},
		)},
		canonical.ObjectPair{Key: "started_at", Value: canonical.NewString(formatISO(s.startedAt))},
		canonical.ObjectPair{Key: "database_path", Value: canonical.NewString(s.path)},
		canonical.ObjectPair{Key: "rate_window_seconds", Value: canonical.NewIntValue(RateWindowSeconds)},
		canonical.ObjectPair{Key: "current_rpm", Value: canonical.NewIntValue(recentStats.Requests)},
		canonical.ObjectPair{Key: "current_tpm", Value: canonical.NewIntValue(recentStats.TotalTokens)},
		canonical.ObjectPair{Key: "router_status", Value: canonical.NewString(routerStatus(recentStats))},
		canonical.ObjectPair{Key: "active_requests", Value: canonical.NewIntValue(int64(s.ActiveCount()))},
		canonical.ObjectPair{Key: "total", Value: totalStats.dict()},
		canonical.ObjectPair{Key: "caller_types", Value: flatStats(groups.callerTypes)},
		canonical.ObjectPair{Key: "models", Value: flatStats(groups.models)},
		canonical.ObjectPair{Key: "requested_models", Value: flatStats(groups.requestedModels)},
		canonical.ObjectPair{Key: "model_requested_models", Value: nestedStats(groups.modelRequested)},
		canonical.ObjectPair{Key: "keys", Value: nestedStats(groups.keys)},
		canonical.ObjectPair{Key: "providers", Value: flatStats(groups.providers)},
		canonical.ObjectPair{Key: "provider_pools", Value: nestedStats(groups.providerPools)},
		canonical.ObjectPair{Key: "upstream_models", Value: flatStats(groups.upstreamModels)},
		canonical.ObjectPair{Key: "unattributed", Value: groups.unattributed.get(dimKey{}).dict()},
	), nil
}

// nullableStringValue 渲染可选字符串为 JSON null 或字符串。
func nullableStringValue(value *string) *canonical.Value {
	if value == nil {
		return canonical.NewNull()
	}
	return canonical.NewString(*value)
}

// KeyStats 对应 key_stats()（metrics.py:252）。
func (s *Store) KeyStats(modelID, keyName string, hours *float64) (*canonical.Value, error) {
	return s.keyStatsSync(modelID, keyName, hours)
}

// keyStatsSync 对应 _key_stats_sync（metrics.py:557）。
func (s *Store) keyStatsSync(modelID, keyName string, hours *float64) (*canonical.Value, error) {
	now := nowBeijing()
	var since *string
	if hours != nil {
		if *hours == 0 {
			// 零小时窗口是"显式为空"：用未来时间戳，避免时钟精度或回拨让刚写入
			// 的记录落进窗口（metrics.py:567 的原注释）。
			value := formatISO(maxBeijing)
			since = &value
		} else {
			value := formatISO(addHours(now, *hours))
			since = &value
		}
	}

	stats, err := s.queryKeyStats(modelID, keyName, since)
	if err != nil {
		return nil, err
	}
	rateSince := formatISO(addSeconds(now, RateWindowSeconds))
	rateStats, err := s.queryKeyStats(modelID, keyName, &rateSince)
	if err != nil {
		return nil, err
	}

	whereParts := []string{"model_id = ?", "key_name = ?"}
	params := []any{modelID, keyName}
	if since != nil {
		whereParts = append(whereParts, "created_at >= ?")
		params = append(params, *since)
	}
	whereSQL := strings.Join(whereParts, " AND ")
	rows, err := s.db().Query(
		"SELECT created_at, status_code, success, retried,\n"+
			"                   prompt_tokens, completion_tokens, total_tokens,\n"+
			"                   cached_tokens, first_token_ms, duration_ms\n"+
			"            FROM request_metrics\n"+
			"            WHERE "+whereSQL+"\n"+
			"            ORDER BY id DESC LIMIT 50",
		params...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	recent := canonical.NewArray()
	for rows.Next() {
		var (
			createdAt                         string
			statusCode                        sql.NullInt64
			success, retried                  int64
			prompt, completion, total, cached int64
			firstToken, duration              int64
		)
		if err := rows.Scan(
			&createdAt, &statusCode, &success, &retried,
			&prompt, &completion, &total, &cached, &firstToken, &duration,
		); err != nil {
			return nil, err
		}
		// 注意：这里的 success / retried 是**整数**，与 request_history 的
		// items 不同（后者过 bool()）。照抄 metrics.py:609-610。
		recent.Arr = append(recent.Arr, canonical.NewObjectOf(
			canonical.ObjectPair{Key: "created_at", Value: canonical.NewString(createdAt)},
			canonical.ObjectPair{Key: "status_code", Value: nullableInt(statusCode)},
			canonical.ObjectPair{Key: "success", Value: canonical.NewIntValue(success)},
			canonical.ObjectPair{Key: "retried", Value: canonical.NewIntValue(retried)},
			canonical.ObjectPair{Key: "prompt_tokens", Value: canonical.NewIntValue(prompt)},
			canonical.ObjectPair{Key: "completion_tokens", Value: canonical.NewIntValue(completion)},
			canonical.ObjectPair{Key: "total_tokens", Value: canonical.NewIntValue(total)},
			canonical.ObjectPair{Key: "cached_tokens", Value: canonical.NewIntValue(cached)},
			canonical.ObjectPair{Key: "first_token_ms", Value: canonical.NewIntValue(firstToken)},
			canonical.ObjectPair{Key: "duration_ms", Value: canonical.NewIntValue(duration)},
		))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "model_id", Value: canonical.NewString(modelID)},
		canonical.ObjectPair{Key: "key_name", Value: canonical.NewString(keyName)},
		canonical.ObjectPair{Key: "stats", Value: stats.dict()},
		canonical.ObjectPair{Key: "current_rpm", Value: canonical.NewIntValue(rateStats.Requests)},
		canonical.ObjectPair{Key: "current_tpm", Value: canonical.NewIntValue(rateStats.TotalTokens)},
		canonical.ObjectPair{Key: "recent_requests", Value: recent},
	), nil
}

// queryKeyStats 对应 _query_key_stats（metrics.py:622）。
func (s *Store) queryKeyStats(modelID, keyName string, sinceCreatedAt *string) (*UsageStats, error) {
	whereParts := []string{"model_id = ?", "key_name = ?"}
	params := []any{modelID, keyName}
	if sinceCreatedAt != nil {
		whereParts = append(whereParts, "created_at >= ?")
		params = append(params, *sinceCreatedAt)
	}
	whereSQL := strings.Join(whereParts, " AND ")

	query := "SELECT " + usageAggregates + " FROM request_metrics WHERE " + whereSQL
	var scan statsScan
	if err := s.db().QueryRow(query, params...).Scan(scan.dests()...); err != nil {
		return nil, err
	}
	stats := scan.stats()

	statusParams := []any{modelID, keyName}
	statusWhere := "model_id = ? AND key_name = ? AND status_code IS NOT NULL"
	if sinceCreatedAt != nil {
		statusWhere += " AND created_at >= ?"
		statusParams = append(statusParams, *sinceCreatedAt)
	}
	statusRows, err := s.db().Query(
		"SELECT status_code, COUNT(*) AS total\n"+
			"            FROM request_metrics\n"+
			"            WHERE "+statusWhere+"\n"+
			"            GROUP BY status_code ORDER BY status_code",
		statusParams...,
	)
	if err != nil {
		return nil, err
	}
	defer statusRows.Close()
	for statusRows.Next() {
		var statusCode sql.NullInt64
		var total int64
		if err := statusRows.Scan(&statusCode, &total); err != nil {
			return nil, err
		}
		stats.StatusCodes[nullStringString(statusCode)] = total
	}
	return stats, statusRows.Err()
}

// RequestHistoryParams 是 request_history() 的参数（metrics.py:263）。
//
// limit / before_id 用指针，以便区分「未传」与「传了 0/负数」——后者要报错。
type RequestHistoryParams struct {
	Hours            *float64
	CallerType       *string
	ModelID          *string
	RequestedModelID *string
	ProviderID       *string
	PoolName         *string
	UpstreamModelID  *string
	KeyName          *string
	StatusCode       *int64
	Success          *bool
	Attributed       *bool
	Limit            int64
	BeforeID         *int64
}

// RequestHistory 对应 request_history()（metrics.py:263）。
func (s *Store) RequestHistory(params RequestHistoryParams) (*canonical.Value, error) {
	now := nowBeijing()

	// 校验顺序照抄：hours → limit → before_id。
	if params.Hours != nil && *params.Hours <= 0 {
		return nil, formatValidationError("hours must be greater than zero or null")
	}
	if params.Limit < 1 || params.Limit > 200 {
		return nil, formatValidationError("limit must be between 1 and 200")
	}
	if params.BeforeID != nil && *params.BeforeID <= 0 {
		return nil, formatValidationError("before_id must be greater than zero")
	}

	var since *string
	if params.Hours != nil {
		value := formatISO(addHours(now, *params.Hours))
		since = &value
	}
	query := queryFilter{
		CallerType:       params.CallerType,
		ModelID:          params.ModelID,
		RequestedModelID: params.RequestedModelID,
		ProviderID:       params.ProviderID,
		PoolName:         params.PoolName,
		UpstreamModelID:  params.UpstreamModelID,
		KeyName:          params.KeyName,
		StatusCode:       params.StatusCode,
		Success:          params.Success,
		Attributed:       params.Attributed,
	}
	untilStr := formatISO(now)
	filter := query.metricFilter(since, &untilStr)
	whereSQL, parameters := filter.sql()

	summary, err := s.queryFilteredStats(whereSQL, parameters)
	if err != nil {
		return nil, err
	}

	var earliest, latest sql.NullString
	var total int64
	boundsQuery := "SELECT MIN(created_at) AS earliest, MAX(created_at) AS latest, COUNT(*) AS total FROM request_metrics" + whereSQL
	if err := s.db().QueryRow(boundsQuery, parameters...).Scan(&earliest, &latest, &total); err != nil {
		return nil, err
	}

	rateSince := formatISO(addSeconds(now, RateWindowSeconds))
	rateWhere, rateParams := query.metricFilter(&rateSince, &untilStr).sql()
	rateStats, err := s.queryFilteredStats(rateWhere, rateParams)
	if err != nil {
		return nil, err
	}

	// 多取一条用于判断 has_more，取完再截断。
	itemWhere := whereSQL
	itemParams := make([]any, len(parameters))
	copy(itemParams, parameters)
	if params.BeforeID != nil {
		itemWhere = appendFilter(itemWhere, "id < ?")
		itemParams = append(itemParams, *params.BeforeID)
	}
	// 两条 LEFT JOIN 取「请求来源」：工作空间与客户端地址各在一张旁挂表里，都不是
	// request_metrics 的列（理由见 schema.go）。LEFT 而不是 INNER：没有归属的行
	// （历史行、不走 proxy 的写入路径）必须照常出现在明细里，来源字段为 null。
	//
	// JOIN 条件里的 request_metrics.id 不写别名：whereSQL 用的是裸列名
	// （见 stats.go 的 metricFilter.sql），给主表起别名会让两边的列名对不上。
	// 两张旁挂表只有 request_id 与自己的列，不存在列名歧义。
	itemQuery := "SELECT id, created_at, caller_type, model_id, requested_model_id,\n" +
		"                   provider_id, pool_name, upstream_model_id, key_name,\n" +
		"                   status_code, success, retried, prompt_tokens,\n" +
		"                   completion_tokens, total_tokens, cached_tokens,\n" +
		"                   cache_creation_input_tokens, cache_read_input_tokens,\n" +
		"                   first_token_ms, duration_ms,\n" +
		"                   w.workspace, s.client_addr, s.user_agent\n" +
		"            FROM request_metrics\n" +
		"            LEFT JOIN request_workspace w ON w.request_id = request_metrics.id\n" +
		"            LEFT JOIN request_source s ON s.request_id = request_metrics.id" +
		itemWhere + "\n" +
		"            ORDER BY id DESC LIMIT ?"
	rows, err := s.db().Query(itemQuery, append(itemParams, params.Limit+1)...)
	if err != nil {
		return nil, err
	}
	items := make([]*canonical.Value, 0, params.Limit)
	hasMore := false
	count := int64(0)
	for rows.Next() {
		if count == params.Limit {
			hasMore = true
			break
		}
		item, err := requestItem(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, item)
		count++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	var nextBeforeID *canonical.Value
	if hasMore && len(items) > 0 {
		nextBeforeID = items[len(items)-1].Lookup("id")
	} else {
		nextBeforeID = canonical.NewNull()
	}

	// window.from：hours 给了就用 since，否则回退到库里的最早时间（可能为 null）。
	var from *canonical.Value
	if since != nil {
		from = canonical.NewString(*since)
	} else {
		from = nullableString(earliest)
	}

	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "count_semantics", Value: canonical.NewString(CountSemantics)},
		canonical.ObjectPair{Key: "window", Value: canonical.NewObjectOf(
			canonical.ObjectPair{Key: "from", Value: from},
			canonical.ObjectPair{Key: "to", Value: canonical.NewString(untilStr)},
			canonical.ObjectPair{Key: "hours", Value: hoursValue(params.Hours)},
		)},
		canonical.ObjectPair{Key: "filters", Value: canonical.NewObjectOf(query.filterPairs()...)},
		canonical.ObjectPair{Key: "rate_window_seconds", Value: canonical.NewIntValue(RateWindowSeconds)},
		canonical.ObjectPair{Key: "current_rpm", Value: canonical.NewIntValue(rateStats.Requests)},
		canonical.ObjectPair{Key: "current_tpm", Value: canonical.NewIntValue(rateStats.TotalTokens)},
		canonical.ObjectPair{Key: "summary", Value: summary.dict()},
		canonical.ObjectPair{Key: "latest_request_at", Value: nullableString(latest)},
		canonical.ObjectPair{Key: "total_items", Value: canonical.NewIntValue(total)},
		canonical.ObjectPair{Key: "items", Value: canonical.NewArray(items...)},
		canonical.ObjectPair{Key: "next_before_id", Value: nextBeforeID},
	), nil
}

// TimeSeriesParams 是 time_series() 的参数（metrics.py:300）。
type TimeSeriesParams struct {
	Hours            float64
	BucketSeconds    int64
	CallerType       *string
	ModelID          *string
	RequestedModelID *string
	ProviderID       *string
	PoolName         *string
	UpstreamModelID  *string
	KeyName          *string
	StatusCode       *int64
	Success          *bool
	Attributed       *bool
}

// TimeSeries 对应 time_series()（metrics.py:300）。
func (s *Store) TimeSeries(params TimeSeriesParams) (*canonical.Value, error) {
	now := nowBeijing()

	if params.Hours <= 0 {
		return nil, formatValidationError("hours must be greater than zero")
	}
	if params.BucketSeconds <= 0 {
		return nil, formatValidationError("bucket_seconds must be greater than zero")
	}
	if seriesPointLimitExceeded(params.Hours, params.BucketSeconds) {
		return nil, formatValidationError("time series cannot exceed %d points", MaxSeriesPoints)
	}

	requestedStart := addHours(now, params.Hours)
	firstEpoch := bucketEpoch(requestedStart, params.BucketSeconds)
	start := fromUnix(firstEpoch)

	query := queryFilter{
		CallerType:       params.CallerType,
		ModelID:          params.ModelID,
		RequestedModelID: params.RequestedModelID,
		ProviderID:       params.ProviderID,
		PoolName:         params.PoolName,
		UpstreamModelID:  params.UpstreamModelID,
		KeyName:          params.KeyName,
		StatusCode:       params.StatusCode,
		Success:          params.Success,
		Attributed:       params.Attributed,
	}
	startStr := formatISO(start)
	untilStr := formatISO(now)
	whereSQL, parameters := query.metricFilter(&startStr, &untilStr).sql()

	bucketParams := []any{
		int64(BucketAnchorEpoch),
		params.BucketSeconds,
		params.BucketSeconds,
		int64(BucketAnchorEpoch),
	}
	bucketExpression := bucketSQL
	allParams := append(append([]any{}, bucketParams...), parameters...)

	buckets := map[int64]*UsageStats{}
	rows, err := s.db().Query(
		"SELECT "+bucketExpression+" AS bucket_epoch, "+usageAggregates+
			" FROM request_metrics"+whereSQL+
			" GROUP BY bucket_epoch ORDER BY bucket_epoch",
		allParams...,
	)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var epoch sql.NullInt64
		dests := append([]any{&epoch}, (&statsScan{}).dests()...)
		var scan statsScan
		dests = append([]any{&epoch}, scan.dests()...)
		if err := rows.Scan(dests...); err != nil {
			rows.Close()
			return nil, err
		}
		if !epoch.Valid {
			// created_at 不是合法日期时 strftime 返回 NULL，整行丢弃。
			continue
		}
		buckets[epoch.Int64] = scan.stats()
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	statusWhere := appendFilter(whereSQL, "status_code IS NOT NULL")
	statusRows, err := s.db().Query(
		"SELECT "+bucketExpression+" AS bucket_epoch, status_code, COUNT(*) AS total"+
			" FROM request_metrics"+statusWhere+
			" GROUP BY bucket_epoch, status_code ORDER BY bucket_epoch, status_code",
		allParams...,
	)
	if err != nil {
		return nil, err
	}
	for statusRows.Next() {
		var epoch sql.NullInt64
		var statusCode sql.NullInt64
		var total int64
		if err := statusRows.Scan(&epoch, &statusCode, &total); err != nil {
			statusRows.Close()
			return nil, err
		}
		stats, ok := buckets[epoch.Int64]
		if !ok {
			stats = &UsageStats{StatusCodes: map[string]int64{}}
			buckets[epoch.Int64] = stats
		}
		stats.StatusCodes[nullStringString(statusCode)] = total
	}
	if err := statusRows.Err(); err != nil {
		statusRows.Close()
		return nil, err
	}
	statusRows.Close()

	// 补齐空桶：从 firstEpoch 到 lastEpoch 每步一点，让前端拿到连续的序列。
	lastEpoch := bucketEpoch(now, params.BucketSeconds)
	points := canonical.NewArray()
	for epoch := firstEpoch; epoch <= lastEpoch; epoch += params.BucketSeconds {
		startedAt := fromUnix(epoch)
		endedAt := startedAt.Add(time.Duration(params.BucketSeconds) * time.Second)
		stats, ok := buckets[epoch]
		if !ok {
			stats = &UsageStats{StatusCodes: map[string]int64{}}
		}
		statsDict := stats.dict()
		point := canonical.NewObjectOf(
			canonical.ObjectPair{Key: "started_at", Value: canonical.NewString(formatISO(startedAt))},
			canonical.ObjectPair{Key: "ended_at", Value: canonical.NewString(formatISO(endedAt))},
			canonical.ObjectPair{Key: "complete", Value: canonical.NewBool(!endedAt.After(now))},
		)
		for _, key := range statsDict.Obj.Keys() {
			value, _ := statsDict.Obj.Get(key)
			point.SetKey(key, value)
		}
		points.Arr = append(points.Arr, point)
	}

	return canonical.NewObjectOf(
		canonical.ObjectPair{Key: "count_semantics", Value: canonical.NewString(CountSemantics)},
		canonical.ObjectPair{Key: "window", Value: canonical.NewObjectOf(
			canonical.ObjectPair{Key: "from", Value: canonical.NewString(startStr)},
			canonical.ObjectPair{Key: "to", Value: canonical.NewString(untilStr)},
			canonical.ObjectPair{Key: "hours", Value: canonical.NewFloat(params.Hours)},
		)},
		canonical.ObjectPair{Key: "filters", Value: canonical.NewObjectOf(query.filterPairs()...)},
		canonical.ObjectPair{Key: "bucket_seconds", Value: canonical.NewIntValue(params.BucketSeconds)},
		canonical.ObjectPair{Key: "points", Value: points},
	), nil
}

// db 返回读连接池；只读查询都走它。
func (s *Store) db() *sql.DB { return s.readDB }

// bucketSQL 是分桶表达式，对应 metrics.py:473。
//
// 用 SQLite 的 strftime 而非在 Go 侧算：created_at 是文本，只有 SQLite 的
// 日期解析规则能保证与参照实现一致（包括对非法日期返回 NULL）。整数除法的
// 截断方向也由 SQLite 决定，跨桶边界的点位才不会位移。
const bucketSQL = "((CAST(strftime('%s', created_at) AS INTEGER) - ?) / ?) * ? + ?"
