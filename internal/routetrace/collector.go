package routetrace

import (
	"encoding/json"
	"sync"
	"time"
)

const (
	maxEvents        = 64
	maxDocumentBytes = 16 * 1024
)

// Collector 持有一次请求的快照，不持有原始凭据或运行时配置。
type Collector struct {
	store     *Store
	id        string
	started   time.Time
	retention time.Duration
	owner     owner
	active    bool // 由 Store.mu 保护：本请求尚未出让在途名额。

	mu         sync.Mutex
	doc        document
	eventSizes []int
	eventBytes int
	finished   bool
	discarded  bool
}

func (c *Collector) ID() string {
	if c == nil {
		return ""
	}
	return c.id
}

// SetRequest 设置已观察到的非空字段；来源未经配置匹配的标签不得传入。
func (c *Collector) SetRequest(request Request) {
	if c == nil {
		return
	}
	request = Request{
		Model: copyLabel(request.Model), Workspace: copyLabel(request.Workspace),
		RouteKind: enum(request.RouteKind, "direct", "alias", "task", "unified"), Stream: copyPtr(request.Stream),
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished || c.discarded {
		return
	}
	if request.Model != nil {
		c.doc.Request.Model = request.Model
	}
	if request.Workspace != nil {
		c.doc.Request.Workspace = request.Workspace
	}
	if request.RouteKind != nil {
		c.doc.Request.RouteKind = request.RouteKind
	}
	if request.Stream != nil {
		c.doc.Request.Stream = request.Stream
	}
}

// SetHTTPStatus 记录已提交状态，不暗示流已经结束。
func (c *Collector) SetHTTPStatus(code int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.finished && !c.discarded && c.doc.Final.HTTPStatus == nil {
		c.doc.Final.HTTPStatus = status(&code)
	}
}

// Add 只能在实际判定后、脱离池锁时调用。
func (c *Collector) Add(event Event) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished || c.discarded {
		return
	}
	if len(c.doc.Events) == maxEvents {
		c.omitLocked(1)
		return
	}
	e, ok := event.safe()
	if !ok {
		c.omitLocked(1)
		return
	}
	e.Seq = len(c.doc.Events) + 1
	e.OffsetMS = max(0, c.store.now().Sub(c.started).Milliseconds())
	body, err := json.Marshal(e)
	if err != nil || c.eventBytes+len(body)+1 > maxDocumentBytes {
		c.omitLocked(1)
		return
	}
	c.eventBytes += len(body) + 1
	c.eventSizes = append(c.eventSizes, len(body)+1)
	c.doc.Events = append(c.doc.Events, e)
}

// UpdateAttempt 补全已发送事件，保留原有顺序和发送时间。
func (c *Collector) UpdateAttempt(event Event) bool {
	if c == nil || event.AttemptNo == nil || *event.AttemptNo <= 0 {
		return false
	}
	event.Kind, event.Sent = "attempt", ptr(true)
	e, _ := event.safe()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished || c.discarded {
		return false
	}
	for i := len(c.doc.Events) - 1; i >= 0; i-- {
		old := c.doc.Events[i]
		if old.Kind != "attempt" || old.AttemptNo == nil || *old.AttemptNo != *e.AttemptNo {
			continue
		}
		e.Seq, e.OffsetMS = old.Seq, old.OffsetMS
		if e.Stage == nil {
			e.Stage = old.Stage
		}
		if e.Protocol == nil {
			e.Protocol = old.Protocol
		}
		if e.Target == nil {
			e.Target = old.Target
		}
		body, err := json.Marshal(e)
		if err != nil || c.eventBytes-c.eventSizes[i]+len(body)+1 > maxDocumentBytes {
			c.omitLocked(1)
			return true
		}
		c.eventBytes += len(body) + 1 - c.eventSizes[i]
		c.eventSizes[i] = len(body) + 1
		c.doc.Events[i] = e
		return true
	}
	return false
}

// Omit 汇总采集端有界缓冲无法保存的事件。
func (c *Collector) Omit(count int) {
	if c == nil || count <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.finished && !c.discarded {
		c.omitLocked(count)
	}
}

func (c *Collector) omitLocked(count int) {
	if !c.doc.Truncated {
		c.store.stats.truncated.Add(1)
	}
	c.doc.Truncated = true
	c.doc.OmittedEvents += min(count, int(^uint(0)>>1)-c.doc.OmittedEvents)
}

// Finish 一次性确定终态，再尝试非阻塞入队；不会访问 SQLite。
func (c *Collector) Finish(outcome string, final Final) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.finished || c.discarded {
		c.mu.Unlock()
		return
	}
	c.finished = true
	now := c.store.now()
	c.doc.Status = "completed"
	c.doc.FinishedAt = ptr(now.UTC())
	c.doc.DurationMS = ptr(max(0, now.Sub(c.started).Milliseconds()))
	c.doc.Outcome = enum(&outcome, "success", "failed", "canceled")
	if c.doc.Outcome == nil {
		c.doc.Outcome = ptr("failed")
	}
	if committed := c.doc.Final.HTTPStatus; committed != nil {
		final.HTTPStatus = committed
	}
	c.doc.Final = Final{
		HTTPStatus: status(final.HTTPStatus), UpstreamStatus: status(final.UpstreamStatus),
		Target: copyTarget(final.Target), StreamEnd: enum(final.StreamEnd, streamEnds...),
	}
	c.fitLocked()
	c.mu.Unlock()
	c.store.enqueue(c)
}

func (c *Collector) discard() {
	c.mu.Lock()
	c.discarded = true
	c.doc.Events, c.eventSizes = nil, nil
	c.mu.Unlock()
}

// fitLocked 预留完整起始和终态，超长时只去掉时间线的尾部。
func (c *Collector) fitLocked() {
	base := c.doc
	base.Events = []Event{}
	body, _ := json.Marshal(base)
	// 省略条数增加时，最多额外占用 int 的十进制位数。
	if len(body)+c.eventBytes <= maxDocumentBytes {
		return
	}
	for len(c.doc.Events) > 0 && len(body)+c.eventBytes+20 > maxDocumentBytes {
		i := len(c.doc.Events) - 1
		c.eventBytes -= c.eventSizes[i]
		c.eventSizes = c.eventSizes[:i]
		c.doc.Events[i] = Event{}
		c.doc.Events = c.doc.Events[:i]
		c.omitLocked(1)
	}
}

func (c *Collector) snapshot() (document, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.discarded {
		return document{}, false
	}
	d := c.doc
	// 对运行中的查询只裁剪副本；查询不能改变最终时间线。
	end, size := len(d.Events), c.eventBytes
	d.Events = []Event{}
	base, _ := json.Marshal(d)
	for end > 0 && len(base)+size+20 > maxDocumentBytes {
		end--
		size -= c.eventSizes[end]
		d.Truncated = true
		if d.OmittedEvents < int(^uint(0)>>1) {
			d.OmittedEvents++
		}
	}
	d.Events = append(d.Events, c.doc.Events[:end]...)
	return d, true
}
