package routetrace

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxRows          = 10000
	maxRunning       = 1024
	maxQueue         = 2048
	maxAge           = 6 * time.Hour
	ownerKeyFilename = "route-trace-owner.key"
)

var (
	ErrUnavailable  = errors.New("追踪暂不可用")
	ErrNotFound     = errors.New("追踪不存在或不可用")
	ErrInvalidID    = errors.New("请求 ID 格式无效")
	ErrUnauthorized = errors.New("凭据无效")
)

type owner struct {
	kind OwnerType
	tag  [sha256.Size]byte
}

func (s Subject) valid() bool {
	return s.Type == OwnerFull || (s.Type == OwnerAccessKey || s.Type == OwnerWorkspaceInference) && s.Credential != ""
}

func computeOwner(subject Subject, key *[sha256.Size]byte) owner {
	o := owner{kind: subject.Type}
	if subject.Type != OwnerFull {
		mac := hmac.New(sha256.New, key[:])
		_, _ = mac.Write([]byte(subject.Type))
		_, _ = mac.Write([]byte{0})
		_, _ = mac.Write([]byte(subject.Credential))
		mac.Sum(o.tag[:0])
	}
	return o
}

func (o owner) admits(viewer owner) bool {
	return viewer.kind == OwnerFull || o.kind == viewer.kind && hmac.Equal(o.tag[:], viewer.tag[:])
}

// DropReason 只允许固定低基数的计数标签。
type DropReason string

const (
	DropUnavailable DropReason = "unavailable"
	DropCapacity    DropReason = "running_capacity"
	DropQueue       DropReason = "queue_full"
	DropRandom      DropReason = "random_unavailable"
	DropCollision   DropReason = "id_collision"
	DropWrite       DropReason = "write_error"
	DropTimeout     DropReason = "running_expired"
)

var dropReasons = [...]DropReason{DropUnavailable, DropCapacity, DropQueue, DropRandom, DropCollision, DropWrite, DropTimeout}

type counters struct {
	admitted    atomic.Uint64
	truncated   atomic.Uint64
	storeErrors atomic.Uint64
	dropped     [len(dropReasons)]atomic.Uint64
}

// Stats 为进程内聚合统计，不含请求或主体标识。
type Stats struct {
	Admitted, Truncated, StoreErrors uint64
	Dropped                          map[DropReason]uint64
	Running, Queued                  int
}

// Store 的热路径只访问内存，打开、维护、写库由同一个后台单写者执行。
type Store struct {
	dir   string
	now   func() time.Time
	stats counters

	mu                      sync.Mutex
	started, closed, create bool
	db                      *sql.DB
	key                     *[sha256.Size]byte
	lock                    *os.File
	stored                  map[string]struct{}
	running                 int
	pending                 map[string]*Collector
	queue                   chan *Collector
	wake                    chan struct{}
	stop                    chan struct{}
	done                    chan struct{}
}

// New 不访问磁盘。空目录保持不可用；不得退回当前工作目录。
func New(cacheDir string) *Store {
	return &Store{
		dir: cacheDir, now: time.Now, pending: make(map[string]*Collector),
		queue: make(chan *Collector, maxQueue), wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
	}
}

// Start 可重复调用；仅 create=true 允许初始化新库，关闭追踪仍可查询历史。
func (s *Store) Start(create bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	changed := s.create != create
	s.create = create
	if !s.started {
		s.started = true
		go s.run()
	} else if changed {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.stop)
	}
	started := s.started
	s.mu.Unlock()
	if started {
		<-s.done
	}
	return nil
}

func (s *Store) drop(reason DropReason) {
	for i, candidate := range dropReasons {
		if candidate == reason {
			s.stats.dropped[i].Add(1)
			return
		}
	}
}

func (s *Store) Stats() Stats {
	out := Stats{Admitted: s.stats.admitted.Load(), Truncated: s.stats.truncated.Load(), StoreErrors: s.stats.storeErrors.Load(), Dropped: make(map[DropReason]uint64, len(dropReasons))}
	for i, reason := range dropReasons {
		out.Dropped[reason] = s.stats.dropped[i].Load()
	}
	s.mu.Lock()
	out.Running, out.Queued = s.running, len(s.queue)
	s.mu.Unlock()
	return out
}

func ValidID(id string) bool {
	if len(id) != 35 || !strings.HasPrefix(id, "rt_") {
		return false
	}
	for _, ch := range id[3:] {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}

// Begin 只在现行开关开启且成功认证后调用；nil 表示不发追踪头。
func (s *Store) Begin(subject Subject, retentionHours int) *Collector {
	if s == nil || !subject.valid() {
		return nil
	}
	if retentionHours < 1 || retentionHours > 168 {
		retentionHours = 24
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.db == nil || s.key == nil {
		s.drop(DropUnavailable)
		return nil
	}
	if s.running >= maxRunning {
		s.drop(DropCapacity)
		return nil
	}
	for range 4 {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			s.drop(DropRandom)
			return nil
		}
		id := "rt_" + hex.EncodeToString(random[:])
		if _, exists := s.stored[id]; exists || s.pending[id] != nil {
			continue
		}
		now := s.now()
		c := &Collector{store: s, id: id, started: now, retention: time.Duration(retentionHours) * time.Hour, owner: computeOwner(subject, s.key),
			doc: document{SchemaVersion: 1, RequestID: id, StartedAt: now.UTC(), Status: "running"}, active: true}
		switch subject.Type {
		case OwnerFull:
			c.doc.Request.CallerType = "local"
		case OwnerAccessKey:
			c.doc.Request.CallerType = "access_key"
		case OwnerWorkspaceInference:
			c.doc.Request.CallerType = "workspace"
		}
		s.pending[id] = c
		s.running++
		s.stats.admitted.Add(1)
		return c
	}
	s.drop(DropCollision)
	return nil
}

func (s *Store) enqueue(c *Collector) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending[c.id] != c || !c.active {
		return
	}
	c.active = false
	s.running--
	select {
	case s.queue <- c:
	default:
		delete(s.pending, c.id)
		c.discard()
		s.drop(DropQueue)
	}
}

// Lookup 在校验格式前拒绝无效主体；调用方必须先实时鉴权。
func (s *Store) Lookup(ctx context.Context, subject Subject, id string) (json.RawMessage, error) {
	if !subject.valid() {
		return nil, ErrUnauthorized
	}
	if !ValidID(id) {
		return nil, ErrInvalidID
	}
	if s == nil {
		return nil, ErrUnavailable
	}
	full := subject.Type == OwnerFull
	s.mu.Lock()
	db, c, key, closed := s.db, s.pending[id], s.key, s.closed
	s.mu.Unlock()
	if db == nil || closed || !full && key == nil {
		return nil, ErrUnavailable
	}
	viewer := computeOwner(subject, key)
	if c != nil {
		if !c.owner.admits(viewer) {
			return nil, ErrNotFound
		}
		doc, ok := c.snapshot()
		if ok && (doc.FinishedAt == nil && s.now().Sub(c.started) < maxAge || doc.FinishedAt != nil && s.now().Before(doc.FinishedAt.Add(c.retention))) {
			return project(doc, full)
		}
	}
	var found owner
	var body []byte
	// CASE 限制读取大小；异常记录既不回显，也不作为合法 API 响应。
	err := db.QueryRowContext(ctx, `SELECT owner_type, owner_tag,
		CASE WHEN length(document_json) <= ? THEN document_json END
		FROM route_traces WHERE request_id = ? AND expires_at > ?`, maxDocumentBytes, id, s.now().UnixNano()).Scan(&found.kind, &bodyTag{&found.tag}, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		s.stats.storeErrors.Add(1)
		return nil, ErrUnavailable
	}
	if !found.admits(viewer) {
		return nil, ErrNotFound
	}
	var doc document
	if json.Unmarshal(body, &doc) != nil || doc.SchemaVersion != 1 || doc.RequestID != id || doc.Status != "completed" {
		s.stats.storeErrors.Add(1)
		return nil, ErrUnavailable
	}
	return project(doc, full)
}

type bodyTag struct{ tag *[sha256.Size]byte }

func (b *bodyTag) Scan(value any) error {
	raw, ok := value.([]byte)
	if !ok || len(raw) != len(b.tag) {
		return ErrUnavailable
	}
	copy(b.tag[:], raw)
	return nil
}

func project(doc document, full bool) (json.RawMessage, error) {
	body, err := doc.project(full)
	if err != nil || len(body) > maxDocumentBytes {
		return nil, ErrUnavailable
	}
	return body, nil
}

func (s *Store) run() {
	defer close(s.done)
	defer s.cleanup()
	retry := time.NewTicker(15 * time.Second)
	defer retry.Stop()
	ttl := time.NewTicker(time.Minute)
	defer ttl.Stop()
	s.refresh()
	for {
		select {
		case <-s.stop:
			for {
				select {
				case c := <-s.queue:
					s.write(c)
				default:
					return
				}
			}
		case <-s.wake:
			s.refresh()
		case <-retry.C:
			s.refresh()
		case <-ttl.C:
			s.expire()
		case c := <-s.queue:
			s.write(c)
		}
	}
}

func (s *Store) cleanup() {
	s.mu.Lock()
	db, lock, traces := s.db, s.lock, s.pending
	s.db, s.lock, s.key, s.pending, s.stored, s.running = nil, nil, nil, nil, nil, 0
	s.mu.Unlock()
	for _, c := range traces {
		c.discard()
	}
	if db != nil {
		_ = db.Close()
	}
	if lock != nil {
		_ = lock.Close()
	}
}

func (s *Store) refresh() {
	s.mu.Lock()
	create, db := s.create, s.db
	s.mu.Unlock()
	if s.dir == "" {
		return
	}
	keyPath, dbPath := filepath.Join(s.dir, ownerKeyFilename), filepath.Join(s.dir, dbFilename)
	if db != nil {
		key, err := readOwnerKey(keyPath)
		if err != nil {
			s.stats.storeErrors.Add(1)
		}
		s.mu.Lock()
		s.key = key
		s.mu.Unlock()
		return
	}
	info, err := os.Lstat(dbPath)
	exists := err == nil
	if !exists && errors.Is(err, os.ErrNotExist) && !create {
		return
	}
	if exists && !info.Mode().IsRegular() || !exists && !errors.Is(err, os.ErrNotExist) {
		s.stats.storeErrors.Add(1)
		return
	}
	if !exists {
		if err := os.MkdirAll(s.dir, 0700); err != nil {
			s.stats.storeErrors.Add(1)
			return
		}
	}
	lock, err := lockInstance(s.dir)
	if err != nil {
		s.stats.storeErrors.Add(1)
		return
	}
	keepLock := false
	defer func() {
		if !keepLock {
			_ = lock.Close()
		}
	}()
	// 另一个实例可能在获取锁之前创建了库；有库时绝不重建 key。
	info, err = os.Lstat(dbPath)
	exists = err == nil
	if exists && !info.Mode().IsRegular() || !exists && (!errors.Is(err, os.ErrNotExist) || !create) {
		return
	}
	key, err := readOwnerKey(keyPath)
	if !exists && errors.Is(err, os.ErrNotExist) {
		key, err = createOwnerKey(keyPath)
	}
	if err != nil {
		s.stats.storeErrors.Add(1)
		if !exists {
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	opened, stored, err := openSQLite(ctx, dbPath, !exists)
	if err != nil {
		s.stats.storeErrors.Add(1)
		return
	}
	keepLock = true
	s.mu.Lock()
	s.db, s.lock, s.key, s.stored = opened, lock, key, stored
	s.mu.Unlock()
	s.expire()
}

func (s *Store) write(c *Collector) {
	s.mu.Lock()
	db, overflow := s.db, len(s.stored)+1-maxRows
	s.mu.Unlock()
	doc, ok := c.snapshot()
	if !ok {
		return
	}
	body, err := project(doc, true)
	var removed map[string]struct{}
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		removed, err = insertRecord(ctx, db, c, doc, body, overflow)
		cancel()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pending, c.id)
	defer c.discard()
	if err != nil {
		s.stats.storeErrors.Add(1)
		s.drop(DropWrite)
		return
	}
	s.stored[c.id] = struct{}{}
	for id := range removed {
		delete(s.stored, id)
	}
}

func (s *Store) expire() {
	now := s.now()
	s.mu.Lock()
	db := s.db
	for id, c := range s.pending {
		if c.active && now.Sub(c.started) >= maxAge {
			delete(s.pending, id)
			s.running--
			c.discard()
			s.drop(DropTimeout)
		}
	}
	s.mu.Unlock()
	if db == nil {
		return
	}
	const batch = 512
	// 条数上限保证这一轮最多删除 maxRows 行，不被持续写入拉长。
	for range maxRows/batch + 1 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		ids, err := deleteExpired(ctx, db, now, batch)
		cancel()
		if err != nil {
			s.stats.storeErrors.Add(1)
			return
		}
		s.mu.Lock()
		for id := range ids {
			delete(s.stored, id)
		}
		s.mu.Unlock()
		if len(ids) < batch {
			return
		}
	}
}
